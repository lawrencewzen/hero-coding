# hero-coding

[English](./README.md) · [中文](./README.zh.md)

面向自动化 coding agent 的极简 harness。把 user story 丢进 `inbox/`,产出 git commits。

## 设计理念

**Harness 是思考层,agent 是可替换的执行器。**

推理能力不必长在模型权重里 —— 长在 loop 里也行。一个非推理模型嵌进带 dispatcher / verifier / reviewer / guardrails 的 harness,整个系统对外就表现为一个能"想清楚"的工程师:目标聚焦、跑偏被抓、失败有具体反馈再重试。

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
   │   Reviewer ── 读 story + verifier 输出 +      │
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
  story.md  ─────  inbox/  ─────►  worker → verifier → reviewer → done/
```

Story 是 Plan 与 Execute 之间的**契约** —— 写下来才能被 Reviewer 引用、被 Verifier 测、被多次 retry 复用。对话式 agent 擅长澄清和迭代,hero-coding 擅长长跑式确定性执行。各做各擅长的事。

Verifier 产出确定性证据(命令退出码),Reviewer 看 verifier 记录 + git diff 之后再判决。Verifier 失败时直接短路成 CHANGES_REQUESTED,不会浪费一次 Reviewer LLM 调用。

## 工作机制

### Dispatcher

监听 `inbox/` 下的 `.md` 文件。新 story 出现时:

1. 解析 YAML frontmatter (id、title、priority、max_retries、verify、scope)。
2. 从 `target_base_ref` 切一个 worktree,分支名 `hero/<id>`。
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
| 自动 rescue commit | Worker 改了文件但忘记 `git commit` | Dispatcher 把 in-scope 改动 commit 掉,让 Reviewer 看得见 |

### Verifier

在 worktree 里跑 story 的 `verify:` shell 命令(没有就 fallback 到 `roles.yaml` 里的 `default_verify`),每条命令独立超时,环境变量 `CI=1`。stdout/stderr 截尾保留;完整输出落到 `runs/<id>-<ts>-verify-r<n>.log`。

Verifier 在自己的 check 上是权威的。任何命令非 0 退出,这一轮直接短路成 CHANGES_REQUESTED,不调 Reviewer LLM。

### Reviewer (Role)

读 user story、Verifier 记录、以及 base ref 之后的 `git log` + `git diff`。调 OpenAI 兼容的 Chat Completions endpoint,带 `response_format: json_object`,返回:

```json
{
  "verdict": "APPROVED" | "CHANGES_REQUESTED",
  "summary": "<一段总结>",
  "ac_check": [{"ac": "...", "satisfied": true|false, "commit": "..."}],
  "comments": [{"file": "...", "line": N, "severity": "blocker"|"nit", "comment": "..."}]
}
```

任何验收标准没明显达成、scope 越界、或 verifier 红了,Reviewer 会标 CHANGES_REQUESTED。它的 reason 会被追加到 story 文件 (`## Reviewer Feedback (auto)` 段落),下一轮 Worker 启动时就能看到。

### Worker / Reviewer 用各自独立的模型

Worker 和 Reviewer 各自在 `config/roles.yaml` 里挑一个 provider。它们可以共享一个 provider,也可以分别指向完全不同的 endpoint。详见下面的 [配置](#配置)。

## 快速开始

```bash
# 1. 编译
go build -o hero ./cmd/hero

# 2. 配置
cp config.local.yaml.example config.local.yaml
# 编辑 config.local.yaml: 填入 API key
# 编辑 config/roles.yaml:  选 worker/reviewer 的 provider 和 target_repo

# 3. 投放 story
cp examples/stories/us-001.md inbox/

# 4. 运行
./hero watch                    # 持续监听 inbox/
./hero run inbox/us-001.md      # 处理单个 story 后退出
```

## 配置

配置由项目根目录下的几个 YAML 文件分层组成。**不需要环境变量,不需要 `export`。** 在项目目录跑 `./hero` 即可,它会读取:

```
config/
  providers/
    ring.yaml          # 一个 provider 一个文件(无 secret,git 追踪)
    gpt-5.yaml
    ...
  roles.yaml           # 哪个 provider 当 worker/reviewer + 运行时旋钮
config.local.yaml      # API key(gitignored,从 .example 拷一份)
```

**Role 的最终 LLM 配置解析顺序**:

```
model            = role.model            OR provider.default_model
reasoning_effort = role.reasoning_effort OR provider.default_reasoning_effort
api_key          = config.local.yaml → keys[<provider name>]
```

### Provider 文件 (`config/providers/ring.yaml`)

```yaml
name: ring                                   # 对应 config.local.yaml 里的 keys.ring
base_url: https://openrouter.ai/api/v1
default_model: inclusionai/ring-2.6-1t:free
default_reasoning_effort: high               # 透传为 OpenAI 风格的 `reasoning_effort` 字段
# insecure_tls: true                         # 仅开发用:接受自签名证书
```

`reasoning_effort` 原样透传 —— OpenAI 系是 `low` / `medium` / `high`,蚂蚁百灵 Ring 是 `high` / `xhigh`,不支持思考档的模型设 `""` 或省略即可。

### Role 分配 (`config/roles.yaml`)

```yaml
worker:
  provider: ring
  # 可选 override —— 取消注释即可覆盖 provider 默认值:
  # model: inclusionai/ring-2.6-1t:free
  # reasoning_effort: xhigh

reviewer:
  provider: ring         # 也可指向不同 provider,得到独立审核

target_repo: examples/target-repo
# target_base_ref: main           # 默认: main
# max_retries: 3
# max_parallel: 2
# verify_timeout_ms: 120000
# default_verify:
#   - go test ./...
```

**切换就改一行**。

| 目标 | 改动 |
|---|---|
| 同 provider 换 worker 模型 | 取消注释并修改 `roles.yaml` 的 `worker.model` |
| 换到完全不同的 provider | 修改 `roles.yaml` 的 `worker.provider` |
| 便宜的 worker + 强的 reviewer | 给两个 role 设不同的 `provider` |
| 接入新 endpoint | 在 `config/providers/` 下放一个新 yaml + 在 `config.local.yaml` 加 `keys.<name>` |
| 切换思考档 | 改 `default_reasoning_effort`(provider 级)或 `reasoning_effort`(role 级) |

### Secrets (`config.local.yaml`)

```yaml
keys:
  ring: sk-or-v1-...
  gpt-5: sk-...
```

**该文件默认 gitignored**(见 `.gitignore`),不要入库。`keys:` 下每个键名要和 provider 文件里的 `name` 字段一致。

### 运行时旋钮(写在 `roles.yaml` 顶层)

| 字段 | 必填 | 默认 | 说明 |
|---|---|---|---|
| `target_repo`     | 是 | — | 目标 git 仓库路径(相对路径以项目根目录为基准) |
| `target_base_ref` | 否 | `main` | 每个 worktree 的 base 分支 / ref |
| `max_retries`     | 否 | `3` | 单 story 的轮次预算(story 自带 `max_retries:` 会覆盖) |
| `max_parallel`    | 否 | `2` | watcher 并发处理的 story 数 |
| `default_verify`  | 否 | `[]` | story 没声明 `verify:` 时的默认命令 |
| `verify_timeout_ms` | 否 | `120000` | Verifier 单条命令的超时(毫秒) |

### 配置如何进入运行中的 Worker

配置是 **进程启动时一次性** 加载、烘焙进每个 Worker 的 HTTP client 的。不存在 hot-reload —— 改了 YAML 必须重启 `hero` 进程才会生效。

```
┌─ 启动期(一次) ───────────────────────────────────────────────────┐
│                                                                   │
│  config/providers/*.yaml ┐                                        │
│  config/roles.yaml       ├→ config.Load(cwd) ──→ *Config          │
│  config.local.yaml       ┘                                        │
│                                                                   │
│  cfg.LLMFor("worker") ──→ LLMConfig                               │
│      model   = role.model            ?: provider.default_model    │
│      effort  = role.reasoning_effort ?: provider.default_effort   │
│      api_key = secrets.keys[provider.name]                        │
│                                                                   │
│  worker.New(LLMConfig) → agent.NewLLMClient → 持久 client         │
│                                                                   │
└───────────────────────────────────────────────────────────────────┘

┌─ 运行期(每个 round) ─────────────────────────────────────────────┐
│                                                                   │
│  Worker.Run() → w.llm.Chat() → POST <base_url>/chat/completions   │
│                  ▲                                                │
│                  └─ 启动时已冻结;运行期不再读 YAML。               │
│                                                                   │
└───────────────────────────────────────────────────────────────────┘
```

Worker 和 Reviewer 各自独立走一次 `LLMFor`,所以它们可以在同一进程里指向不同 provider。`worker.New` 里还有第三层 (Go 代码层) model 覆盖钩子 —— `role.Role.Model`,留给未来想"按 story 类型选模型"这类逻辑用,不需要碰 YAML。

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
| `max_retries` | 否 | 覆盖 `roles.yaml` 里 `max_retries` 的 story 级值 |
| `verify` | 否 | Verifier 每轮跑的 shell 命令。可写成平铺 list(单 "default" tier)或有序 map(分层),见下面 |
| `scope` | 否 | Glob 模式列表,auto-rescue commit 只 stage 这些路径(其它文件不动) |

`Out of Scope` 跟 `Goal` 一样重要 —— 它防止 agent "顺手"重构无关代码。

### 分层 `verify:`(推荐)

平铺 list 每轮跑所有命令。**对大项目建议声明 named tier**,按声明顺序跑,**tier 间 fail-fast** —— 编译挂了不用浪费分钟级的 integration 测试:

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

**tier 内**所有命令都跑(让作者一次看到所有 lint 错,不是只看第一个);**tier 间**只要一个 tier 有失败,后面的 tier 全部跳过。Reviewer prompt 上 APPROVED 时静默 —— 不会把每条成功命令的输出灌进 LLM context —— 所以分层不会膨胀 token。

## 目录结构

```
cmd/hero/                  CLI 入口
config/                    YAML 配置(providers + role 分配)
internal/
  agent/                   OpenAI 兼容 Chat Completions 客户端
  config/                  YAML loader + 角色级 LLM 解析
  dispatcher/              orchestrator + git worktree 管理 + inbox watcher
  reviewer/                code review verdict (verifier 短路 + LLM)
  logging/                 slog 帮手
  role/                    Role 抽象 (system prompt + 允许的工具 + model)
  state/                   per-story 持久化状态 (原子 JSON)
  story/                   frontmatter 解析 + reviewer feedback 追加
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
