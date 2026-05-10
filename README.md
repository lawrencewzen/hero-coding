# hero-coding

[English](./README.md) · [中文](./README.zh.md)

A minimal harness for autonomous coding agents. Drop a user story in `inbox/`, get git commits out.

## Philosophy

**The harness is the thinking layer. The agent is a replaceable executor.**

Reasoning lives in the loop, not in the weights. A non-reasoning model running inside a harness with dispatcher, verifier, reviewer, and guardrails behaves like a reasoning system as a whole — focused on the task, caught when it strays, retried with concrete feedback.

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
   │   Reviewer ── reads story + verifier output   │
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
  story.md  ─────  inbox/  ─────►  worker → verifier → reviewer → done/
```

The story is the **contract** between Plan and Execute — written down so the Reviewer can reference it, the Verifier can test it, and retries can replay against the same target. Conversational agents are good at clarifying and iterating; hero-coding is good at long-running deterministic execution. Each does what it's best at.

The Verifier produces deterministic evidence (test exit codes); the Reviewer reads the verifier record + git diff before deciding. Verifier failure short-circuits to CHANGES_REQUESTED without burning a Reviewer LLM call.

## How It Works

### Dispatcher

Watches `inbox/` for `.md` files. When a story appears it:

1. Parses the YAML frontmatter (id, title, priority, max_retries, verify, scope).
2. Creates a git worktree on branch `hero/<id>` from `target_base_ref`.
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
| Auto-rescue commit | Worker edited files but forgot to `git commit` | Dispatcher commits in-scope changes so Reviewer can see them |

### Verifier

Runs the story's `verify:` shell commands (or the `default_verify` fallback from `roles.yaml`) inside the worktree with a per-command timeout and CI=1 in env. Stdout/stderr are captured and tailed; full output goes to `runs/<id>-<ts>-verify-r<n>.log`.

The Verifier is authoritative on its checks. If any command exits non-zero, the round short-circuits to CHANGES_REQUESTED without calling the Reviewer LLM.

### Reviewer (Role)

Reads the user story, the Verifier record, and `git log` + `git diff` since the base ref. Calls an OpenAI-compatible Chat Completions endpoint with `response_format: json_object` and returns:

```json
{
  "verdict": "APPROVED" | "CHANGES_REQUESTED",
  "summary": "<one paragraph>",
  "ac_check": [{"ac": "...", "satisfied": true|false, "commit": "..."}],
  "comments": [{"file": "...", "line": N, "severity": "blocker"|"nit", "comment": "..."}]
}
```

The Reviewer marks CHANGES_REQUESTED for a round if any Acceptance Criterion isn't visibly satisfied, scope was violated, or the verifier was red. Its reason is appended to the story file (`## Reviewer Feedback (auto)`) so the next Worker round sees it.

### Worker / Reviewer use independent models

Worker and Reviewer each pick a provider in `config/roles.yaml`. They can share one provider, or point at completely different endpoints. See [Configuration](#configuration) below.

## Quick Start

```bash
# 1. Build
go build -o hero ./cmd/hero

# 2. Configure
cp config.local.yaml.example config.local.yaml
# edit config.local.yaml: paste your API key(s)
# edit config/roles.yaml:  pick worker/reviewer providers + target_repo

# 3. Drop a story
cp examples/stories/us-001.md inbox/

# 4. Run
./hero watch                 # watch inbox/ continuously
./hero run inbox/us-001.md   # process one story and exit
```

## Configuration

Configuration is layered across YAML files at the project root. **No environment variables, no `export`.** Run `./hero` from the project directory and it reads:

```
config/
  providers/
    ring.yaml          # one file per LLM provider (no secrets, git-tracked)
    gpt-5.yaml
    ...
  roles.yaml           # which provider plays worker/reviewer + runtime knobs
config.local.yaml      # API keys (gitignored — copy from .example)
```

**Resolution order** when computing a role's effective LLM config:

```
model            = role.model            OR provider.default_model
reasoning_effort = role.reasoning_effort OR provider.default_reasoning_effort
api_key          = config.local.yaml → keys[<provider name>]
```

### Provider file (`config/providers/ring.yaml`)

```yaml
name: ring                                   # matches keys.ring in config.local.yaml
base_url: https://openrouter.ai/api/v1
default_model: inclusionai/ring-2.6-1t:free
default_reasoning_effort: high               # forwarded as OpenAI-style `reasoning_effort`
# insecure_tls: true                         # dev-only: accept self-signed certs
```

`reasoning_effort` is forwarded verbatim — `low` / `medium` / `high` for OpenAI-family models, `high` / `xhigh` for Ant Ling Ring, `""` (or omitted) for models that don't support a thinking budget.

### Role assignment (`config/roles.yaml`)

```yaml
worker:
  provider: ring
  # Optional overrides — uncomment to override the provider's defaults:
  # model: inclusionai/ring-2.6-1t:free
  # reasoning_effort: xhigh

reviewer:
  provider: ring         # or point at a different provider for an independent verdict

target_repo: examples/target-repo
# target_base_ref: main           # default: main
# max_retries: 3
# max_parallel: 2
# verify_timeout_ms: 120000
# default_verify:
#   - go test ./...
```

**Switching is one line.**

| Goal | Change |
|---|---|
| Swap worker model on the same provider | uncomment + edit `worker.model` in `roles.yaml` |
| Swap worker to a different provider | edit `worker.provider` in `roles.yaml` |
| Cheap worker + strong reviewer | use different `provider` for each role |
| Add a new endpoint | drop a new `config/providers/<name>.yaml` + add `keys.<name>` in `config.local.yaml` |
| Switch reasoning effort | edit `default_reasoning_effort` (provider) or `reasoning_effort` (role) |

### Secrets (`config.local.yaml`)

```yaml
keys:
  ring: sk-or-v1-...
  gpt-5: sk-...
```

This file is **gitignored** by default (see `.gitignore`). Keep it out of version control. The key under `keys:` must match the provider's `name` field.

### Runtime knobs (top-level in `roles.yaml`)

| Field | Required | Default | Description |
|---|---|---|---|
| `target_repo`     | yes | — | Path to the target git repo (relative paths resolve against the project root) |
| `target_base_ref` | no  | `main` | Branch / ref each worktree is cut from |
| `max_retries`     | no  | `3` | Round budget per story (story-level `max_retries:` overrides) |
| `max_parallel`    | no  | `2` | Concurrent stories the watcher will process |
| `default_verify`  | no  | `[]` | Default verifier commands when a story has no `verify:` |
| `verify_timeout_ms` | no | `120000` | Per-command timeout for the verifier |

### How config flows into a running worker

Configuration is loaded **once at process startup** and frozen into each Worker's HTTP client. There is no hot-reload — edits to YAML require a `hero` restart to take effect.

```
┌─ Startup (once) ────────────────────────────────────────────────┐
│                                                                  │
│  config/providers/*.yaml ┐                                       │
│  config/roles.yaml       ├→ config.Load(cwd) ──→ *Config         │
│  config.local.yaml       ┘                                       │
│                                                                  │
│  cfg.LLMFor("worker") ──→ LLMConfig                              │
│      model   = role.model            ?: provider.default_model   │
│      effort  = role.reasoning_effort ?: provider.default_effort  │
│      api_key = secrets.keys[provider.name]                       │
│                                                                  │
│  worker.New(LLMConfig) → agent.NewLLMClient → persistent client  │
│                                                                  │
└──────────────────────────────────────────────────────────────────┘

┌─ Runtime (every round) ─────────────────────────────────────────┐
│                                                                  │
│  Worker.Run() → w.llm.Chat() → POST <base_url>/chat/completions  │
│                  ▲                                               │
│                  └─ frozen at startup; YAML never re-read here.  │
│                                                                  │
└──────────────────────────────────────────────────────────────────┘
```

Worker and Reviewer each go through `LLMFor` independently, so they can sit on different providers within the same process. There is also a third (Go-level) model-override hook in `worker.New` via `role.Role.Model`, which lets future code pick a model per story without touching YAML.

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
| `max_retries` | no | Overrides `roles.yaml`'s `max_retries` for this story |
| `verify` | no | Shell commands the Verifier runs after each round. Either a flat list (single "default" tier) or an ordered map of named tiers — see below |
| `scope` | no | Glob patterns the auto-rescue commit will stage (others left untouched) |

`Out of Scope` is as important as `Goal` — it stops the agent from "helpfully" refactoring unrelated code.

### Layered `verify:` (recommended)

A flat list runs every command on every round. For larger projects you can declare named tiers — they run in declaration order with **fail-fast between tiers**, so a broken build doesn't waste minutes of integration tests:

```yaml
verify:
  build:
    - go build ./...
  lint:
    - go vet ./...
    - golangci-lint run
  unit:
    - go test ./...
  e2e:
    - go test -tags=integration ./...
```

Within a tier all commands still run (so the author sees every lint error at once, not just the first); between tiers the verifier short-circuits as soon as one tier has a failure. The Reviewer's prompt is silent on success — no per-command output is dumped when everything passes — so layering doesn't bloat the LLM context.

## Layout

```
cmd/hero/                  CLI entry point
config/                    YAML configuration (providers + role assignment)
internal/
  agent/                   OpenAI-compatible Chat Completions client
  config/                  YAML loader + per-role LLM resolution
  dispatcher/              orchestrator + git worktree mgmt + inbox watcher
  reviewer/                code-review verdict (verifier short-circuit + LLM)
  logging/                 slog helpers
  role/                    Role abstraction (system prompt + allowed tools + model)
  state/                   per-story persistent stats (atomic JSON)
  story/                   frontmatter parser + reviewer-feedback appender
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
