# hero-coding

A minimal harness for autonomous coding agents. Drop a user story in `inbox/`, get git commits out.

## Philosophy

**The harness is the thinking layer. The agent is a replaceable executor.**

Reasoning-heavy models aren't the only path to reliable code generation. When a non-reasoning model runs inside a harness with dispatcher, judge, and guardrails, the system as a whole becomes the "thinker" — keeping the agent focused, catching mistakes, and retrying with feedback. The intelligence lives in the loop, not in the weights.

## Architecture

```
inbox/us-001.md        # user story (markdown + frontmatter)
      │
      ▼
  Dispatcher  ─── watches inbox/, creates an isolated git worktree per story
      │
      ▼
   Worker     ─── coding agent (pi CLI in --mode json)
      │           atomic git commits per change
      ▼
   Judge      ─── reads git log + diffs, returns {verdict, reason}
      │
      ├─ PASS ─→ done/us-001.md
      └─ FAIL ─→ append reason to story, retry (up to MAX_RETRIES)
```

Dispatcher / Worker / Judge are **stateless one-shot processes**. Each story runs in its own git worktree branched from `TARGET_BASE_REF` (default `main`). All durable state lives in git + filesystem.

## How It Works

### Dispatcher

Watches `inbox/` for `.md` files. When a new user story appears:

1. Parses the frontmatter (id, title, priority, max_retries).
2. Creates a dedicated git worktree on a branch `hero/{story-id}`.
3. Enters the PASS-FAIL loop: spawn Worker → run Judge → if FAIL, append feedback to story and retry.
4. On PASS, moves the story to `done/`. On exhaustion (all retries failed), records the run as `gave_up`.

### Worker

Spawns a coding agent as a child process. The agent receives the user story body as its prompt and works the codebase through tool calls (read, write, edit, bash, git). Every meaningful edit is committed atomically.

**Guardrails** (enforced per round, ~50 lines of code):

| Guardrail | Trigger | Action |
|---|---|---|
| Tool-call cap | 80 tool calls in one round | Kill worker |
| Wall-time cap | 5 minutes elapsed | Kill worker |
| Loop detection | Same tool signature ≥4× in 6-call window | Kill worker |
| Auto-rescue commit | Worker modified files but forgot to `git commit` | Dispatcher commits them so Judge can see the work |

The loop detector catches the most common failure mode: a non-reasoning model getting stuck repeating the same action (e.g. `echo "done"` in a loop). The auto-rescue commit handles the case where the worker made the right changes but skipped the commit — without it, the Judge would see no commits and fail the round.

### Judge

Reads the full git context since the base ref: commit log + complete diff. Sends it together with the user story to an LLM via OpenAI-compatible API. Returns a structured verdict:

```json
{"verdict": "PASS", "reason": "…"}
{"verdict": "FAIL", "reason": "concrete, actionable feedback for next round"}
```

The Judge checks each Acceptance Criteria against the actual diffs. It is not a rubber stamp — it will fail a round if the worker didn't commit, modified out-of-scope files, or only partially addressed the criteria.

### PASS-FAIL Loop

```
round 1: Worker → Judge FAIL → append feedback → round 2
round 2: Worker (sees feedback) → Judge PASS → done/
```

At each FAIL, the Judge's reason is appended to the story file. The next Worker round sees the full story including all previous feedback, giving it context to correct course. The loop continues until PASS or `max_retries` is exhausted.

## Quick Start

```bash
npm install
cp .env.example .env
# edit .env: WORKER_PROVIDER, WORKER_MODEL, JUDGE_BASE_URL, JUDGE_API_KEY,
#            JUDGE_MODEL, TARGET_REPO, optionally TARGET_BASE_REF

# prepare a target repo with a seed commit
npm run setup-target

# drop a user story
cp examples/stories/us-001.md inbox/

# watch and run
npm run watch
```

## Configuration

Worker and Judge use independent model configurations. Both speak the **OpenAI-compatible Chat Completions API** — any model that implements this protocol works (OpenAI, Anthropic via proxy, open-source models via vLLM/Ollama, etc.).

- **Worker** uses [pi-coding-agent](https://github.com/badlogic/pi-mono/tree/main/packages/coding-agent) CLI. Configure providers in `pi-config/models.json`.
- **Judge** uses the OpenAI SDK directly. Configure via `JUDGE_BASE_URL`, `JUDGE_API_KEY`, `JUDGE_MODEL` in `.env`.

### Adding a Model Provider

Edit `pi-config/models.json`:

```json
{
  "providers": {
    "my-provider": {
      "baseUrl": "https://api.example.com/v1",
      "api": "openai-completions",
      "apiKey": "MY_API_KEY",
      "compat": {
        "supportsDeveloperRole": false,
        "supportsReasoningEffort": false
      },
      "models": [
        { "id": "my-model-id", "contextWindow": 128000, "maxTokens": 16000 }
      ]
    }
  }
}
```

Then set `WORKER_PROVIDER=my-provider` and `WORKER_MODEL=my-model-id` in `.env`. The `apiKey` field references an environment variable name (e.g. `MY_API_KEY`), so add `MY_API_KEY=…` to `.env` as well.

## User Story Format

```markdown
---
id: us-001
title: Add timezone parameter to formatDate
created: 2026-04-28T09:00
priority: normal
max_retries: 3
---

## Goal
Add an optional `timezone` parameter to `formatDate` in `src/utils.ts`.

## Acceptance Criteria
- [ ] Function signature accepts `timezone?: string` (default `"UTC"`)
- [ ] Existing callers continue to work unchanged
- [ ] Add 3 tests in `tests/utils.test.ts` covering UTC / specific tz / default
- [ ] `npm test` passes

## Constraints
- Do not modify other files
- Keep TypeScript strict mode

## Out of Scope
- Locale / formatting style changes
```

`Out of Scope` is as important as `Goal` — it prevents the agent from "helpfully" refactoring unrelated code. The `max_retries` field overrides the global `MAX_RETRIES` default per story.

## License

MIT
