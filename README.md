# hero-coding

[English](./README.md) · [中文](./README.zh.md)

A minimal harness for autonomous coding agents. Drop a user story in `inbox/`, get git commits out.

## Philosophy

**The harness is the thinking layer. The agent is a replaceable executor.**

Reasoning lives in the loop, not in the weights. A non-reasoning model running inside a harness with dispatcher, verifier, judge, and guardrails behaves like a reasoning system as a whole — focused on the task, caught when it strays, retried with concrete feedback.

## Architecture

```
inbox/us-001.md
       │
       ▼
   Dispatcher  ── watches inbox/, creates an isolated git worktree per story
       │
       ▼
   ┌── Round Loop ─────────────────────────────────┐
   │                                               │
   │   Worker   ── coding agent, native ReAct loop │
   │     │        atomic git commits per change    │
   │     ▼                                         │
   │   Verifier ── runs the story's `verify:`      │
   │     │        commands (tests / lint / type)   │
   │     ▼                                         │
   │   Judge    ── reads story + verifier output   │
   │              + git diff, returns PASS / FAIL  │
   │                                               │
   └─────────────────┬─────────────────────────────┘
                     │
              ┌──────┴──────┐
            PASS           FAIL
              │             │
              ▼             ▼
          done/        feedback appended → next round
```

This is a deliberately reduced **Execute-Verify** harness — the simplified form of the PEV (Plan-Execute-Verify) pattern with the Plan stage left outside the harness boundary. No LLM Planner is run inside hero-coding, by design — see `docs/architecture-research-2026.md` §E.4 for the rationale.

**Where Plan happens.** Plan typically lives in an upstream conversation between the user and an interactive coding agent (Claude Code, Codex, Cursor, etc.). The user discusses the change, the agent helps clarify scope and acceptance criteria, and the output of that session is committed as a story file (`inbox/us-XXX.md`). hero-coding picks up where that conversation ends:

```
user ⇄ Claude Code / Codex          hero-coding
  │       (Plan)                      (Execute + Verify)
  │                                       │
  ▼                                       ▼
  story.md  ─────  inbox/  ─────►  worker → verifier → judge → done/
```

The story is the **contract** between Plan and Execute — written down so the Judge can reference it, the Verifier can test it, and retries can replay against the same target. Conversational agents are good at clarifying and iterating; hero-coding is good at long-running deterministic execution. Each does what it's best at.

The Verifier produces deterministic evidence (test exit codes); the Judge reads the verifier record + git diff before deciding. Verifier failure short-circuits to FAIL without burning a Judge LLM call.

## How It Works

### Dispatcher

Watches `inbox/` for `.md` files. When a story appears it:

1. Parses the YAML frontmatter (id, title, priority, max_retries, verify, scope).
2. Creates a git worktree on branch `hero/<id>` from `TARGET_BASE_REF`.
3. Runs the round loop until PASS or `max_retries` exhausted.
4. On PASS, moves the story to `done/`. On exhaustion, records the run as `gave_up`.

State is persisted as JSON in `runs/state/<id>.json` after every round, so a crashed dispatcher resumes from the last completed round on restart.

### Worker (Role)

A native ReAct loop driving an OpenAI-compatible Chat Completions endpoint. Tools (bash, read_file, write_file, edit_file, grep, ls) come from the `internal/tools` package and execute in the worktree.

The worker's behaviour is configured by a **Role** — system prompt + allowed-tools whitelist + optional model override. The default Worker role:
- Hard-coded git-commit rule: every edit must be followed by `git commit` before any further reasoning.
- Filesystem-as-context tool set (per Microsoft Azure SRE finding: bash + file primitives + grep beat 100+ specialised tools).

**Schema-level tool restriction**: the LLM only sees the tools its Role allows. Disallowed tools physically do not exist in the schema, so the model cannot call them in the first place.

**Guardrails** (defense-in-depth):

| Guardrail | Trigger | Action |
|---|---|---|
| Tool-call cap | 80 calls in one round | Kill round |
| Wall-time cap | 5 minutes | Kill round |
| Loop detection | Same tool+args ≥4× in 6-call window | Kill round |
| Auto-rescue commit | Worker edited files but forgot to `git commit` | Dispatcher commits in-scope changes so Judge can see them |

### Verifier

Runs the story's `verify:` shell commands (or the `HERO_DEFAULT_VERIFY` fallback) inside the worktree with a per-command timeout and CI=1 in env. Stdout/stderr are captured and tailed; full output goes to `runs/<id>-<ts>-verify-r<n>.log`.

The Verifier is authoritative on its checks. If any command exits non-zero, the round short-circuits to FAIL without calling the Judge LLM.

### Judge (Role)

Reads the user story, the Verifier record, and `git log` + `git diff` since the base ref. Calls an OpenAI-compatible Chat Completions endpoint with `response_format: json_object` and returns:

```json
{"verdict": "PASS", "reason": "…"}
{"verdict": "FAIL", "reason": "concrete, actionable feedback for next round"}
```

The Judge fails a round if any Acceptance Criterion isn't visibly satisfied, scope was violated, or the verifier was red. Its reason is appended to the story file (`## Captain Feedback (auto)`) so the next Worker round sees it.

### Worker / Judge use independent models

Worker and Judge each bind to a `<provider>/<model>` pair via env. They can share one provider, or point at completely different endpoints. See [Configuration](#configuration) below.

## Quick Start

```bash
# 1. Build
go build -o hero ./cmd/hero

# 2. Configure
cp .env.example .env
# edit .env: WORKER_*, JUDGE_*, TARGET_REPO

# 3. Drop a story
cp examples/stories/us-001.md inbox/

# 4. Run
./hero watch          # watch inbox/ continuously
./hero run inbox/us-001.md   # process one story and exit
```

## Configuration

All configuration is via environment variables (loaded from `.env` if present). The model is two-layered: define **providers** once, then **bind roles** to a `<provider>/<model>` pair.

### Providers

```bash
# Pattern: HERO_PROVIDER_<name>_{BASE_URL,API_KEY,INSECURE_TLS}
HERO_PROVIDER_coproxy_BASE_URL=https://localhost:8443/v1
HERO_PROVIDER_coproxy_API_KEY=sk-...
HERO_PROVIDER_coproxy_INSECURE_TLS=true   # dev-only: accept self-signed certs

HERO_PROVIDER_openai_BASE_URL=https://api.openai.com/v1
HERO_PROVIDER_openai_API_KEY=sk-...
```

`<name>` may be any `[A-Za-z0-9._-]+` identifier. `INSECURE_TLS` is optional (default `false`); use it only for local self-signed proxies.

### Role bindings

```bash
HERO_WORKER=coproxy/gpt-5.4    # provider/model
HERO_JUDGE=openai/gpt-5.5
```

**Switching is one line.**

| Goal | Change |
|---|---|
| Swap worker model on the same provider | `HERO_WORKER=coproxy/claude-4.7` |
| Swap worker to a different provider | `HERO_WORKER=openai/gpt-5.5` |
| Cheap worker + strong judge (or vice versa) | Edit both lines |
| One-off run without touching `.env` | `HERO_WORKER=openai/gpt-5.5 ./hero run inbox/us-001.md` |
| Add a new endpoint | Add three `HERO_PROVIDER_<name>_*` lines, then bind |

### Other settings

| Var | Required | Default | Description |
|---|---|---|---|
| `TARGET_REPO`     | yes | — | Absolute path to the target git repo |
| `TARGET_BASE_REF` | no  | `main` | Branch / ref each worktree is cut from |
| `MAX_RETRIES`     | no  | `3` | Round budget per story (story-level `max_retries:` overrides) |
| `MAX_PARALLEL`    | no  | `2` | Concurrent stories the watcher will process |
| `HERO_DEFAULT_VERIFY` | no | (empty) | Newline-separated default verifier commands when a story has no `verify:` |
| `HERO_VERIFY_TIMEOUT_MS` | no | `120000` | Per-command timeout for the verifier |

## User Story Format

```markdown
---
id: us-001
title: Add timezone parameter to formatDate
priority: normal
max_retries: 3
verify:
  - npm test
  - npm run typecheck
scope:
  - src/**
  - tests/**
---

## Goal
Add an optional `timezone` parameter to `formatDate` in `src/utils.ts`.

## Acceptance Criteria
- [ ] `formatDate(date, timezone?: string)` — second argument is optional, defaults to `"UTC"`.
- [ ] When timezone is omitted or `"UTC"`, output is byte-identical to today.
- [ ] When a valid IANA tz string is passed, date is formatted in that zone.
- [ ] Add 3 tests in `tests/utils.test.ts`.
- [ ] `npm test` passes.

## Constraints
- Do not modify other functions in `src/utils.ts`.
- Keep TypeScript strict mode happy.

## Out of Scope
- Locale / month-name changes.
```

Frontmatter fields:

| Field | Required | Description |
|---|---|---|
| `id` | yes | Must match `[A-Za-z0-9][A-Za-z0-9._-]*` (used as git branch suffix) |
| `title` | yes | Human-readable title |
| `priority` | no | `low` \| `normal` \| `high` (default `normal`) |
| `max_retries` | no | Overrides `MAX_RETRIES` env var for this story |
| `verify` | no | Shell commands the Verifier runs after each round |
| `scope` | no | Glob patterns the auto-rescue commit will stage (others left untouched) |

`Out of Scope` is as important as `Goal` — it stops the agent from "helpfully" refactoring unrelated code.

## Layout

```
cmd/hero/                  CLI entry point
internal/
  agent/                   OpenAI-compatible Chat Completions client
  config/                  env-driven configuration
  dispatcher/              orchestrator + git worktree mgmt + inbox watcher
  judge/                   PEV verdict (verifier short-circuit + LLM)
  logging/                 slog helpers
  role/                    Role abstraction (system prompt + allowed tools + model)
  state/                   per-story persistent stats (atomic JSON)
  story/                   frontmatter parser + judge-feedback appender
  tooldef/                 Tool interface
  tools/                   bash, read_file, write_file, edit_file, grep, find, ls, read_tracker
  verifier/                deterministic shell-command runner
  worker/                  ReAct loop, guardrails, schema-level tool filter, JSONL trace
inbox/                     drop user stories here
done/                      stories that reached PASS land here
runs/                      per-run JSON, verifier logs, JSONL traces, state/
worktrees/                 one git worktree per active story
examples/                  sample stories + a target repo
```

## License

MIT
