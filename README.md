# hero-coding

Minimal autonomous coding agent MVP. Drop a user story in `inbox/`, get git commits out.

## Architecture

```
inbox/us-001.md        # user story (markdown + frontmatter)
      │
      ▼
  Dispatcher  ─── watches inbox/, runs one story at a time
      │
      ▼
   Worker     ─── pi-coding-agent in --mode json
      │           atomic git commits per change
      ▼
   Judge      ─── reads git log + diffs, returns {verdict, reason}
      │
      ├─ PASS ─→ done/us-001.md
      └─ FAIL ─→ append reason to story, retry (up to MAX_RETRIES)
```

Dispatcher / Worker / Judge are **stateless one-shot processes**. All state lives in git + filesystem.

Models for Worker and Judge are configured independently via `.env` — both speak the OpenAI-compatible Chat Completions API.

## Quick Start

```bash
npm install
cp .env.example .env
# edit .env: WORKER_*, JUDGE_*, TARGET_REPO

# drop a user story
cp examples/stories/us-001.md inbox/

# watch and run
npm run watch
```

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

## License

MIT
