# hero-coding

[English](./README.md) · [中文](./README.zh.md)

面向自动化 coding agent 的极简 harness。把 user story 丢进 `inbox/`,产出 git commits。

## 设计理念

**Harness 是思考层,agent 是可替换的执行器。**

推理能力不必长在模型权重里 —— 长在 loop 里也行。一个非推理模型嵌进带 dispatcher / verifier / judge / guardrails 的 harness,整个系统对外就表现为一个能"想清楚"的工程师:目标聚焦、跑偏被抓、失败有具体反馈再重试。

## 架构

```
inbox/us-001.md
       │
       ▼
   Dispatcher  ── 监听 inbox/,每个 story 起一个隔离的 git worktree
       │
       ▼
   ┌── Round Loop ─────────────────────────────────┐
   │                                               │
   │   Worker   ── coding agent,原生 ReAct loop    │
   │     │        每次有意义的修改都立即 git commit  │
   │     ▼                                         │
   │   Verifier ── 跑 story 里 `verify:` 声明的     │
   │     │        命令(测试 / lint / typecheck)    │
   │     ▼                                         │
   │   Judge    ── 读 story + verifier 输出 +      │
   │              git diff,返回 PASS / FAIL        │
   │                                               │
   └─────────────────┬─────────────────────────────┘
                     │
              ┌──────┴──────┐
            PASS           FAIL
              │             │
              ▼             ▼
          done/        反馈追加进 story → 下一轮
```

这是一个**有意精简过的 Execute-Verify** harness —— PEV (Plan-Execute-Verify) 模式的简化版,Plan 阶段被刻意放在 harness 边界之外。hero-coding 内部不跑 LLM Planner,这是设计决策,理由见 `docs/architecture-research-2026.md` §E.4。

**Plan 阶段在哪里发生。** Plan 通常发生在用户与对话式 coding agent (Claude Code、Codex、Cursor 等) 的上游会话里。用户讨论需求,agent 帮忙澄清范围和验收标准,会话产物落成一个 story 文件 (`inbox/us-XXX.md`)。hero-coding 从这个对话结束的地方接手:

```
user ⇄ Claude Code / Codex          hero-coding
  │       (Plan)                      (Execute + Verify)
  │                                       │
  ▼                                       ▼
  story.md  ─────  inbox/  ─────►  worker → verifier → judge → done/
```

Story 是 Plan 与 Execute 之间的**契约** —— 写下来才能被 Judge 引用、被 Verifier 测、被多次 retry 复用。对话式 agent 擅长澄清和迭代,hero-coding 擅长长跑式确定性执行。各做各擅长的事。

Verifier 产出确定性证据(命令退出码),Judge 看 verifier 记录 + git diff 之后再判决。Verifier 失败时直接短路成 FAIL,不会浪费一次 Judge LLM 调用。

## 工作机制

### Dispatcher

监听 `inbox/` 下的 `.md` 文件。新 story 出现时:

1. 解析 YAML frontmatter (id、title、priority、max_retries、verify、scope)。
2. 从 `TARGET_BASE_REF` 切一个 worktree,分支名 `hero/<id>`。
3. 进入 round loop,直到 PASS 或 `max_retries` 用完。
4. PASS 时把 story 移到 `done/`;用完仍未 PASS 时记录为 `gave_up`。

每轮结束后状态以 JSON 形式写到 `runs/state/<id>.json`,所以 dispatcher 崩了重启可以从最后一轮已完成的位置 resume。

### Worker (Role)

原生 ReAct loop,后端是 OpenAI 兼容的 Chat Completions endpoint。工具 (bash、read_file、write_file、edit_file、grep、ls) 来自 `internal/tools` 包,在 worktree 里执行。

Worker 的行为通过 **Role** 配置 —— system prompt + 工具白名单 + 可选的 model 覆盖。默认 Worker role:
- 强制 git-commit 规则:每次有意义的修改之后,必须先 `git commit` 再做任何后续推理。
- Filesystem-as-context 工具集 (Microsoft Azure SRE 实测:bash + 文件原语 + grep 胜过 100+ 特化工具,Intent Met 从 45% 提升到 75%)。

**Schema 层工具限制**:LLM 只看得见它的 Role 允许的工具。被禁工具在 schema 里物理上不存在,模型根本没法调用。

**Guardrails** (defense-in-depth 第二道防线):

| 守护栏 | 触发条件 | 动作 |
|---|---|---|
| 工具调用上限 | 一轮 80 次 | 杀掉这一轮 |
| 墙时间上限 | 5 分钟 | 杀掉这一轮 |
| 循环检测 | 同样 tool+args 在 6 次窗口内出现 ≥4 次 | 杀掉这一轮 |
| 自动 rescue commit | Worker 改了文件但忘记 `git commit` | Dispatcher 把 in-scope 改动 commit 掉,让 Judge 看得见 |

### Verifier

在 worktree 里跑 story 的 `verify:` shell 命令(没有就 fallback 到 `HERO_DEFAULT_VERIFY`),每条命令独立超时,环境变量 `CI=1`。stdout/stderr 截尾保留;完整输出落到 `runs/<id>-<ts>-verify-r<n>.log`。

Verifier 在自己的 check 上是权威的。任何命令非 0 退出,这一轮直接短路 FAIL,不调 Judge LLM。

### Judge (Role)

读 user story、Verifier 记录、以及 base ref 之后的 `git log` + `git diff`。调 OpenAI 兼容的 Chat Completions endpoint,带 `response_format: json_object`,返回:

```json
{"verdict": "PASS", "reason": "…"}
{"verdict": "FAIL", "reason": "下一轮可执行的具体反馈"}
```

任何验收标准没明显达成、scope 越界、或 verifier 红了,Judge 都会判 FAIL。它的 reason 会被追加到 story 文件 (`## Captain Feedback (auto)` 段落),下一轮 Worker 启动时就能看到。

### Worker / Judge 用各自独立的模型

`WORKER_*` 和 `JUDGE_*` 是两组独立的 env,配两个不同的 LLM endpoint。Worker 用便宜快速的模型、Judge 用更强的模型,或者反过来,都行。Role 抽象让"按角色覆盖 model"成本几乎为零。

## 快速开始

```bash
# 1. 编译
go build -o hero ./cmd/hero

# 2. 配置
cp .env.example .env
# 编辑 .env: WORKER_*, JUDGE_*, TARGET_REPO

# 3. 投放 story
cp examples/stories/us-001.md inbox/

# 4. 运行
./hero watch                    # 持续监听 inbox/
./hero run inbox/us-001.md      # 处理单个 story 后退出
```

## 配置

所有配置走环境变量(若存在 `.env` 会自动加载)。

| 变量 | 必填 | 默认 | 说明 |
|---|---|---|---|
| `WORKER_BASE_URL` | 是 | — | Worker LLM endpoint (OpenAI 兼容) |
| `WORKER_API_KEY`  | 是 | — | Worker API key |
| `WORKER_MODEL`    | 是 | — | Worker model id |
| `JUDGE_BASE_URL`  | 是 | — | Judge LLM endpoint |
| `JUDGE_API_KEY`   | 是 | — | Judge API key |
| `JUDGE_MODEL`     | 是 | — | Judge model id |
| `TARGET_REPO`     | 是 | — | 目标 git 仓库的绝对路径 |
| `TARGET_BASE_REF` | 否 | `main` | 每个 worktree 的 base 分支 / ref |
| `MAX_RETRIES`     | 否 | `3` | 单 story 的轮次预算(story 自带 `max_retries:` 会覆盖) |
| `MAX_PARALLEL`    | 否 | `2` | watcher 并发处理的 story 数 |
| `HERO_DEFAULT_VERIFY` | 否 | (空) | story 没声明 `verify:` 时的默认命令(用换行分隔) |
| `HERO_VERIFY_TIMEOUT_MS` | 否 | `120000` | Verifier 单条命令的超时 |

## User Story 格式

```markdown
---
id: us-001
title: 给 formatDate 加 timezone 参数
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
给 `src/utils.ts` 的 `formatDate` 加一个可选的 `timezone` 参数。

## Acceptance Criteria
- [ ] `formatDate(date, timezone?: string)` —— 第二个参数可选,默认 `"UTC"`。
- [ ] 不传 timezone 或传 `"UTC"` 时,输出与今天逐字节一致。
- [ ] 传合法 IANA tz 字符串时,日期按该时区格式化。
- [ ] 在 `tests/utils.test.ts` 加 3 个测试。
- [ ] `npm test` 通过。

## Constraints
- 不要改 `src/utils.ts` 里其它函数。
- 保持 TypeScript strict 模式编译通过。

## Out of Scope
- 语言/月份名变化。
```

Frontmatter 字段:

| 字段 | 必填 | 说明 |
|---|---|---|
| `id` | 是 | 必须匹配 `[A-Za-z0-9][A-Za-z0-9._-]*` (用作 git 分支后缀) |
| `title` | 是 | 人类可读的标题 |
| `priority` | 否 | `low` \| `normal` \| `high` (默认 `normal`) |
| `max_retries` | 否 | 覆盖此 story 的 `MAX_RETRIES` |
| `verify` | 否 | Verifier 每轮跑的 shell 命令 |
| `scope` | 否 | Glob 模式列表,auto-rescue commit 只 stage 这些路径(其它文件不动) |

`Out of Scope` 跟 `Goal` 一样重要 —— 它防止 agent "顺手"重构无关代码。

## 目录结构

```
cmd/hero/                  CLI 入口
internal/
  agent/                   OpenAI 兼容 Chat Completions 客户端
  config/                  env 驱动的配置
  dispatcher/              orchestrator + git worktree 管理 + inbox watcher
  judge/                   PEV 判决 (verifier 短路 + LLM)
  logging/                 slog 帮手
  role/                    Role 抽象 (system prompt + 允许的工具 + model)
  state/                   per-story 持久化状态 (原子 JSON)
  story/                   frontmatter 解析 + judge feedback 追加
  tooldef/                 Tool interface
  tools/                   bash, read_file, write_file, edit_file, grep, find, ls, read_tracker
  verifier/                确定性 shell 命令 runner
  worker/                  ReAct loop, guardrails, schema 层工具过滤, JSONL trace
inbox/                     往这里丢 user story
done/                      达到 PASS 的 story 落到这里
runs/                      per-run JSON、verifier 日志、JSONL trace、state/
worktrees/                 每个活跃 story 一个 git worktree
examples/                  示例 story + 目标仓库
```

## License

MIT
