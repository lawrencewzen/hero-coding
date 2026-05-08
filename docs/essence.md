# hero-coding 精读

按读懂顺序排,每节"做什么 + 关键流程 + 最简骨架代码(去掉边界处理和日志)"。完整 ~6300 行的项目压成下面这一份。

适合 4 类场景:
1. 想快速 onboarding 整个项目
2. 改代码前定位职责边界
3. 给别人做技术分享前对账
4. 一段时间没碰回来找回手感

## 怎么读这份文档

- **代码骨架**(`go` 块)是**简化过**的:省略了 error handling / logging / 部分边界判断,目的是让你看清主干。改代码请回原文件,**不要照骨架抄**。
- **行内 `//` 注释**解释**这一行为什么这么写**,而不是"在做什么"。"做什么"看代码本身就够。
- **🔍 易混点** 标记真实踩过或经常被读者搞反的细节。
- **🚩 踩坑记录** 是真实出现过的 bug 还原 + 当时的修法,看完比读代码本身收获大。
- **🔗 跨节引用** 提示"这里和那里必须放一起看"。
- 章节顺序 = 心智模型构建顺序,**不要跳着读**。

---

## 0. 一图看全

```
┌──── inbox/us-XXX.md ────┐
│                         │
▼                         │
Dispatcher                │
  │ parse + load state    │
  │ git worktree add      │
  ▼                       │
┌── Round Loop ───────────┐
│                         │
│   Worker                │  ◄── PEV 三阶段 (Plan 在 harness 外)
│     │  (ReAct loop)     │
│     ▼                   │
│   Verifier              │
│     │  (deterministic)  │
│     ▼                   │
│   Judge                 │
│     │  (LLM verdict)    │
│     │                   │
│     ├─ PASS ──→ done/   │
│     └─ FAIL ──→ feedback│
└──────┬──────────────────┘
       │
       ▼
   state.Write (atomic JSON, resumable)
```

| 模块                                         | 职责                              | 关键文件                            |
| ------------------------------------------ | ------------------------------- | ------------------------------- |
| `cmd/hero`                                 | CLI 入口,signal 处理                | `main.go`                       |
| `dispatcher`                               | 编排 + worktree + watcher         | `dispatcher.go` `git.go` `watch.go` |
| `worker`                                   | ReAct + schema 限工具 + guardrails | `worker.go` `guardrails.go`     |
| `verifier`                                 | 跑 shell 命令拿确定性证据                | `verifier.go`                   |
| `judge`                                    | 短路或 LLM 判 PASS/FAIL             | `judge.go`                      |
| `agent`                                    | OpenAI-compat HTTP client       | `llm.go`                        |
| `tools`                                    | bash/read/write/edit/grep/ls(沙箱) | `tools/*.go`                    |
| `state` `story` `role` `config` `provider` | 数据/配置                           | 各自一两个小文件                        |

### 🔍 易混点:这不是 PEV,是 EV

很多人看到 Plan-Execute-Verify(PEV)就以为里面有个 LLM Planner。**hero-coding 内部没有 Plan 阶段**:Plan 发生在用户跟 Claude Code / Codex 的对话里,产物是 story 文件;harness 从 story 进 inbox 这一刻接手,只做 Execute 和 Verify。

为什么这么设计?调研 §E.4:**story 已经是 plan,YAGNI**。让 LLM 再做一次 Plan 等于让被改造者参与定义"怎么算改造完",方法论上自欺欺人。

---

## 1. 数据流转 = 4 个关键类型

```
Story (frontmatter + body)
   │
   ▼
RunStats (per-story 账本)  ◄──── state.Load/Write/Clear
   │
   ├─ WorkerRunStats[]    (per-round)
   ├─ VerifierRecord[]    (per-round)
   └─ VerdictRecord[]     (per-round)
```

```go
// state/stats.go — 账本骨架,只列关键字段
type Stats struct {
    StoryID, Branch, BaseSha, WorktreePath string  // 「身份证」+ 工作区位置
    WorkerRuns    []WorkerRunStats   // 每轮 worker 的指标(token / 工具调用 / 时长)
    Verifications []VerifierRecord   // 每轮 verifier 跑了什么、结果如何
    Verdicts      []VerdictRecord    // 每轮 judge 的判决
    FinalStatus   string              // "done" | "gave_up" | "running"  ← resume 的唯一信号
}
```

### 心智模型

`Stats` 是事实表,`WorkerRuns` / `Verifications` / `Verdicts` 三个数组**按 round 同步增长**。round N 跑完应该满足:

```
len(WorkerRuns) == len(Verifications) == len(Verdicts) == N   (PASS 提前 break 时也成立)
```

`FinalStatus="running"` 是 resume 的唯一触发条件,见 §3。

### 🔍 易混点:这三个数组不是日志,是**结构化事件流**

每一项都是结构化记录(typed struct),能直接喂给 judge 当 prompt、给 dashboard 渲染、给 cost analyzer 累加 token。**不要把 WorkerRunStats 当 stdout 截屏**——它是约束过 schema 的一手数据。

### 🔗 跨节

- `BaseSha` 字段在 §3 的 resume 路径里被校验,见"🚩 踩坑:baseSha resume 失效"
- `FinalStatus` 在 §3 的 cleanup defer 里决定 worktree 是否保留

---

## 2. 配置 = Provider × RoleBinding

```
env: HERO_PROVIDER_<name>_BASE_URL/API_KEY/INSECURE_TLS
        ↓
   Provider{Name, BaseURL, APIKey, InsecureTLS}    Registry: map[name]→Provider
                                                       │
env: HERO_<ROLE>=<provider>/<model>                    │
        ↓                                              │
   Binding{Provider, Model} ──────────────────────────┘
                                                       ↓
                                           cfg.LLMFor("worker")
                                                       ↓
                            LLMConfig{BaseURL, APIKey, Model, InsecureTLS}
```

| 切换需求      | 改动                                |
| --------- | --------------------------------- |
| 换 worker 模型 | `HERO_WORKER=coproxy/claude-4.7` |
| 换 provider   | `HERO_WORKER=openai/gpt-5.5`     |
| 加新 endpoint | 加 3 行 `HERO_PROVIDER_<name>_*`   |
| 一次性覆盖     | `HERO_WORKER=... ./hero run ...` |

### 为什么是两层(Provider × Binding)而不是一层?

朴素方案:每个 role 自己的 BASE_URL/API_KEY/MODEL 三件套(`WORKER_BASE_URL`、`JUDGE_BASE_URL`...)。这样写**3 个 role 共用一个 endpoint 时要重复 3 遍**;TLS 那种 quirk 也要每个 role 写一遍。

两层结构的好处:
- **endpoint 信息(URL / key / TLS)只写一次**,role 通过短字符串 `coproxy/gpt-5.4` 引用
- 加 Reviewer / Planner 等新 role 时只多一行 `HERO_REVIEWER=...`,不必重复 endpoint 三件套
- TLS quirk 挂在 provider 上,**自然按 endpoint 隔离**(见下面易混点)

### 🔍 易混点:`INSECURE_TLS` 是 provider 维度,不是全局

```go
// agent/llm.go
if cfg.InsecureTLS {
    transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}  // 这个 transport 是 client 自己的 clone
}
```

每个 LLMClient 拿到自己 transport 的 clone。所以**就算 coproxy 端开了 InsecureTLS,judge 走 openai endpoint 仍然走严格 TLS 校验**。

朴素的 `NODE_TLS_REJECT_UNAUTHORIZED=0`(我们之前 TS 版用过)是**进程级**全局开关——一旦开启,所有 outgoing TLS 都不校验。这是安全噩梦。两层结构天然把这种 quirk 限制在出问题的那一个 endpoint。

### 🚩 踩坑记录:`HERO_DEFAULT_VERIFY` 不能用 `:` 分隔

最初设计是冒号分隔(`npm test:npm run lint`),后来发现用户写 `npm test:curl http://...` 时被静默截断成 4 段。修正为换行分隔,因为冒号在命令里太常见(URL、`npm run test:unit` 这种 script 名)。

```bash
# ❌ 错的:HERO_DEFAULT_VERIFY=go test:go vet
# ✅ 对的:
HERO_DEFAULT_VERIFY="go test ./...
go vet ./..."
```

---

## 3. Dispatcher = round loop + worktree 守门

### 流程图

```
  RunOnce(storyPath)
       │
       ▼
  story.Parse + state.Load
       │
       ├─ resume? && baseSha 仍存在
       │     YES → 继续旧 worktree(必要时 rebuild)
       │     NO  → clear stale state(见踩坑记录)
       │
       ▼
  prepareWorktree (git worktree add -b hero/<id>)
       │
       ▼
  ┌─── for round in 1..limit ─────┐
  │                                │
  │  worker.Run(ctx, story, ...)   │
  │     ↓                          │
  │  autoRescueCommit              │ ← 兜底:worker 改了文件忘 commit
  │     ↓                          │
  │  verifier.Run                  │
  │     ↓                          │
  │  judge.Run                     │
  │     ↓                          │
  │  state.Write (每轮持久化)        │
  │                                │
  │  PASS → break, FinalStatus=done│
  │  FAIL → 追加 feedback 到 story │
  └────────────────────────────────┘
       │
       ▼
  state.WriteRun + (PASS) mv inbox/→done/
  defer cleanupWorktree if FinalStatus != "running"
```

### 骨架(行内有解释)

```go
func (d *Dispatcher) RunOnce(ctx, storyPath) (*Stats, error) {
    s := story.Parse(...)               // YAML frontmatter + markdown body
    prior := state.Load(...)             // 可能为 nil(首次跑)或损坏(踩坑记录里展开)
    resume := prior.FinalStatus == "running"

    // 关键防线:state 还在但 target-repo 已被 setup-target.sh 重置过
    // 旧的 baseSha 可能已经不存在,直接 resume 会在 git worktree add 时崩
    if resume && !rev_parse(prior.BaseSha) {
        state.Clear(...); resume = false   // 当作首次跑,丢弃 stale state
    }

    worktree, baseSha := prepare or restoreFromPrior(...)
    stats := initOrResume(prior, ...)

    // defer 决定 worktree 命运:
    // - FinalStatus 终态(done / gave_up) → 清理
    // - 仍在 running(中途崩) → 保留,下次 resume 用
    defer cleanupIfTerminal()

    for round := startRound; round <= limit; round++ {
        w := d.worker.Run(...)              // PEV 之 E:执行(LLM)
        autoRescueCommit(worktree, scope)   // 关键兜底 ← 解释见下面
        v := verifier.Run(...)              // PEV 之 V1:确定性证据
        j := judge.Run(..., verifier=v)     // PEV 之 V2:LLM 判决(可能短路)
        if j == FAIL {
            // 把 judge 的 reason 追加到 story 文件,下一轮 worker 看得到
            feedback = j.reason + verifier.Summarize(v)
            appendToStory(round, feedback)
        }
        state.Write(stats)                   // 每轮原子持久化,崩了能 resume
        if j == PASS { stats.FinalStatus = "done"; break }
    }
    if stats.FinalStatus == "running" { stats.FinalStatus = "gave_up" }
    state.WriteRun(...)                       // 最终 run 记录到 runs/<id>-<ts>.json
    if PASS { mv inbox/ → done/ }
}
```

### 为什么 autoRescueCommit 在 worker 跟 verifier 之间?

LLM 改了文件**经常忘记 git commit**(尤其 round 末尾被截停时)。如果不 commit:
- verifier 跑出来的 `git diff base..HEAD` 是空的(改动还在 working tree 里)
- judge 看到 0 commits 必然 FAIL,理由是"没有改动"
- 但实际 worker 已经把代码改对了,下一轮被这个误判搞崩

autoRescueCommit 的逻辑:**verifier 之前**扫一遍 `git status`,有 dirty file 就强制 commit(尊重 story.scope,只 stage 范围内的文件)。

放这个位置的原因 = **必须在 verifier 看 working tree 之前完成**,否则 verifier 也会跑出错(比如 `go test` 看到 syntax error 是因为 worker 写了一半的文件还没 commit,但其实 worker 已经在 worktree 里改完了)。

### 🚩 踩坑记录:resume 时 baseSha 死链(defense-in-depth 修法)

**bug 复现**:
1. 第一次跑 us-001,coproxy 断了,worker 中途崩,state 留在 `running`,记录的 baseSha 是 `ff4d25b...`
2. 我手动跑 `setup-target.sh`,`rm -rf .git && git init` 重置 target-repo
3. 重新跑 hero,它检测到 `state.FinalStatus == "running"` → 走 resume 分支
4. 拿 baseSha `ff4d25b...` 喂给 `git worktree add`
5. **fatal: invalid reference: ff4d25b...**(因为新的 .git 完全不知道这个 commit)

**思考过的 5 种修法**:

| 方案                                   | 评价                              |
| ------------------------------------ | ------------------------------- |
| A. setup-target.sh 末尾删 hero 状态     | 治本,但只覆盖走脚本的场景               |
| B. dispatcher 入口校验 baseSha          | 自愈,但只治症状的一种切片;每次启动 spawn git |
| C. canResume 谓词收口所有前置条件         | 可读性 + 可扩展                       |
| D. 不存 baseSha,resume 时实时 rev-parse | 极简,但语义改:resume 会跑在更新的 base 上 |
| E. catch git 错误后 fallback           | 错误处理散乱,日志混乱                  |

**最终修法 = A + C 组合**(defense in depth):

**层 1 — root cause**(`scripts/setup-target.sh`):

```bash
rm -rf "$ROOT/runs/state" "$ROOT/runs/traces" "$ROOT/worktrees"
echo "[setup-target] wiped hero runtime state"
```

99% 走 setup 脚本的场景在这里就清干净了,dispatcher 启动时根本不会有 stale state。

**层 2 — symptom net**(`internal/dispatcher/dispatcher.go`):

```go
// 把所有 resume 前置条件收成一个有名谓词
func (d *Dispatcher) canResume(ctx, prior) string {
    if _, err := git("rev-parse", "--verify", prior.BaseSha+"^{commit}"); err != nil {
        return fmt.Sprintf("baseSha %s no longer exists: %v", prior.BaseSha, err)
    }
    return ""    // 空字符串 = 可以 resume
}

// 调用处
if resume {
    if reason := d.canResume(ctx, prior); reason != "" {
        d.log.Warn("resume preconditions failed", "reason", reason)
        state.Clear(...); prior = nil; resume = false
    }
}
```

兜住所有非 setup-target 路径:用户手敲 `git reset --hard <earlier>`、`rm -rf .git && git init`、远端 force-push、把 base ref 改名……

**为什么不用方案 D(不存 baseSha)?**

存的好处是 round 之间 base 不漂移。如果不存:

```
round 1: base = main = abc123
worker 改了点东西
round 1 完成,state.Write
... 用户在 main 上 push 了几个 commit, main 变成 def456
round 2 拿 main → base = def456
judge 看 git diff base..HEAD → 看到 worker 没做的改动也在范围里
```

这种漂移会让 judge 的"改动范围"判断错乱,debug 难。所以**存 baseSha + 校验**比**不存**更安全。

**教训**:
- **resume 不能 trust state**,必须验外部世界(git repo)还跟 state 记录一致
- **defense in depth > single layer**,setup 脚本 + dispatcher 检查双保险,任何一层失守另一层兜
- 改语义换简单是**糟糕权衡**,语义微妙变化常常成为后续 bug 的源头

### 🚩 踩坑记录:state 文件损坏不能静默当 nil

最初的 `state.Load` 实现:任何 error → 返回 nil。看起来很 graceful。

但 `appendJudgeFeedback` 用过非原子 write(写到一半崩了 → JSON 截断)。下一次 `state.Load` 解析失败 → 返回 nil → dispatcher 当首次跑 → **几个 round 的进度史无声丢失**。

**修法**:区分 ENOENT vs JSON parse error:

```go
raw, err := os.ReadFile(file)
if err != nil {
    if errors.Is(err, fs.ErrNotExist) { return nil, nil }   // 真的没有 → fresh start
    return nil, err                                          // 其它 IO 错 → 报告
}
var s Stats
if err := json.Unmarshal(raw, &s); err != nil {
    // 文件存在但损坏 → quarantine 让人看,绝不静默
    os.Rename(file, file+".corrupt-"+timestamp)
    return nil, fmt.Errorf("state corrupt: %w", err)
}
```

**教训**:**"不存在"和"损坏"的处理必须不同**,合并成一个 catch-all 一定丢数据。

### 🔗 跨节

- worker 在 §4,verifier 在 §5,judge 在 §6
- watcher / 并发模型在 §9
- state 持久化在 §1 的 `Stats` 类型

---

## 4. Worker = 自己写的 ReAct loop

整个项目最值得反复读的一节。

### 流程图

```
build toolDefs ── 按 role.AllowedTools 过滤 (schema 层) ──┐
                                                         │
[system: role.SystemPrompt] ──┬── msgs                   │
[user: story.body + feedback]─┘                          │
                                                         ▼
┌──── for iter in 1..MAX_ITERATIONS ───────────────────────┐
│                                                          │
│  resp ← llm.Chat(ctx{wallTimeout}, msgs, toolDefs)       │
│  msgs += resp                                            │
│                                                          │
│  if resp.ToolCalls is empty:                             │
│    break  ◄── normal stop (model decided "done")         │
│                                                          │
│  for each tc in resp.ToolCalls:                          │
│    if guard.observe(tc) != "" → kill round              │
│    result := tools[tc.Name].Execute(tc.args)             │
│    msgs += {role:"tool", tool_call_id:tc.id, content:r}  │
│                                                          │
└──────────────────────────────────────────────────────────┘
```

### 骨架(行内重注释)

```go
func (w *Worker) Run(ctx, opts) (Stats, error) {
    allowed := w.role.AllowedTools

    // === schema 层限工具(关键!不是 runtime guard)===
    // 不在 allowed 里的工具压根不进 toolDefs,LLM 看不见也调不出来
    // 这是物理层面的约束,而非"求 LLM 别调"
    tools := buildTools(workDir)
    toolDefs := []map[string]any{}
    for _, t := range tools {
        if _, ok := allowSet[t.Name]; ok {
            toolDefs = append(toolDefs, t.Definition())
        }
        // 注意:即使 schema 不暴露,toolByName 仍然记录全部
        // 是为了 guardrails 的 tool_violation 兜底(理论上模型瞎编名字时能抓住)
    }

    // 系统消息 + 用户消息;feedback 是上一轮 judge FAIL 的 reason
    msgs := []ChatMessage{
        {Role: "system", Content: composeSystemPrompt(w.role.SystemPrompt, allowed)},
        {Role: "user",   Content: storyBody + maybeFeedback},
    }
    guard := newGuardrails(allowed)

    // 5 分钟墙时间硬上限,防 LLM 卡死或网络挂
    wallCtx, cancel := WithTimeout(ctx, MaxWallTime)
    defer cancel()

    for iter := 0; iter < MaxIterations; iter++ {
        resp := w.llm.Chat(wallCtx, msgs, toolDefs)
        msgs = append(msgs, resp)             // 注意:resp 整个进消息历史,不只是 text

        // 模型主动停 = 没 tool_calls 了 = 它认为 done
        if len(resp.ToolCalls) == 0 { break }

        for _, tc := range resp.ToolCalls {
            // 关键拦截点:执行前先过 guardrails
            // 任何 kill 立即返回,不执行该工具(即便 LLM 已经"决定"调它了)
            if kill := guard.observe(tc); kill != "" {
                stats.KillReason = kill
                return                         // 整个 round 标记 FAIL
            }
            result := toolByName[tc.Name].Execute(tc.args)
            // tool 结果作为 ChatMessage 回灌(role="tool"),LLM 下一轮能看见
            msgs = append(msgs, {Role:"tool", ToolCallID: tc.ID, Content: result})
        }
    }
}
```

### Defense-in-depth 4 层(背下来这表)

| 层           | 机制                                             | 失败模式                       |
| ----------- | ---------------------------------------------- | -------------------------- |
| **Prompt**  | system prompt 写"你只能用 X, Y, Z"                  | 模型可能不遵循                    |
| **Schema**  | LLM 看到的 `tools[]` 只含 allowed —— **物理上不存在被禁工具** | 模型即使想用也调不出                 |
| **Runtime** | `guardrails.observe()` 调用前拦截                   | 兜底 schema 漏网 / 调用循环 / 总数超限 |
| **Tool**    | `sandboxPath` 限制路径在 worktree 内                 | 路径穿越 / symlink 逃逸          |

🔍 **易混点:Schema 层 vs Runtime 层**——以前的实现是 schema 全暴露(让 LLM 自由调),guardrails 在调用后看到不该用就 kill。这是**事后拦截**,模型已经"决策"过、消耗过 token、思路已经被污染。

现在的 schema 层过滤 = **事前不让选项出现**。模型从来没有"想用 git push"的机会,因为它根本没看到这个工具。

研究文档 §B.4 直接引用 March 2026 论文:

> "write tools do not exist in its schema, **not because a runtime check blocks the attempt**."

### Guardrails 4 种 kill

| 名字              | 触发条件                            | 数字                  |
| --------------- | ------------------------------- | ------------------- |
| `tool_cap`      | 单轮工具调用 > 80                     | 80                  |
| `wall_timeout`  | 单轮墙时间 > 5min                    | 通过 `ctx WithTimeout` |
| `loop`          | 同 `name+args` 在最近 6 次调用里出现 ≥ 4 次 | window=6, threshold=4 |
| `tool_violation` | 工具名不在 allowed 里(schema 失守的兜底)  | —                   |

🔍 **易混点:`loop` 的 sig 是 name + args,不只是 name**

```go
sig := tc.Name + "|" + json.Marshal(tc.Args)
```

LLM 卡死的常见模式是 `bash("git status")` 调 4 遍想看不同结果。如果只比 name,worker 第一轮就被 kill 了(因为它真的合理地连续调几次 bash)。比 name+args 才能区分"卡死"和"正常多次调用"。

### 🚩 踩坑记录:工具名 mismatch

最初版 worker 的 `DefaultAllowed` 写成 `["bash", "read", "edit", "write", "grep", "ls"]`(沿用 TS 时代风格),但 vendor 进来的 Alice tools 名字其实是 `read_file` / `write_file` / `edit_file`(带 `_file` 后缀)。

结果:
- LLM 收到 schema 里只有 `read_file`(因为我们 vendor 的 tool 自己声明的是这名字)
- 但 allowSet 里只有 `read`
- LLM 调 `read_file` → guardrails 看到不在 allowed 里 → kill `tool_violation`
- 一直 fail,完全跑不动

**修法**:统一使用 vendor tool 的实际名字。新 `DefaultAllowed` = `["bash", "read_file", "edit_file", "write_file", "grep", "ls"]`。

**教训**:schema 层的 allowed list **必须跟 tool.Definition().Function.Name 一字不差**。建议加单元测试遍历 `buildTools()` 检查每个 default name 都在 register 里,但目前没做。

### 🔗 跨节

- LLM client 在 §7
- 工具沙箱在 §8
- Role 配置在 §2 的 RoleBinding(model 字段可覆盖 provider 的默认)

---

## 5. Verifier = 确定性的 shell runner(分层 + fail-fast)

### 数据形态:tier 列表

`story.Frontmatter.Verify` 是 `[]VerifyTier`,每个 tier 一组命令。两种 YAML 形态都接受:

```yaml
# 形态 A:平铺 list → 包成单 "default" tier
verify:
  - go test ./...
  - go vet ./...

# 形态 B:有序 map → 多 tier,声明顺序保留
verify:
  build: [go build ./...]
  lint: [go vet ./..., golangci-lint run]
  unit: [go test ./...]
  e2e:  [go test -tags=integration ./...]
```

### 执行规则(Codex / Augment 同款)

- **tier 间 fail-fast**:tier N 任何一条命令失败,tier N+1 起整体 skip(record 仍写入,标记 Skipped)
- **tier 内 run-to-completion**:同一 tier 的命令全部跑完,即便第 1 条已经红 —— 让作者一次看到所有 lint 错而非只看一个

### 骨架(行内重注释)

```go
func Run(ctx, opts) (VerifierRecord, error) {
    tiers := opts.Story.Verify
    if len(tiers) == 0 && len(opts.DefaultCommands) > 0 {
        // HERO_DEFAULT_VERIFY 走这条:把 []string 包成单 "default" tier
        tiers = []VerifyTier{{Name: "default", Commands: opts.DefaultCommands}}
    }
    if len(tiers) == 0 { return Skipped }    // skip 不算失败,默认 PASS

    earlyFail := false
    for _, tier := range tiers {
        if earlyFail {
            // 后续 tier 整体跳过,但每条 cmd 都生成一个 Skipped record
            // 让 Summarize 能告诉 worker"unit 因为 lint 失败被跳过了"
            for _, cmd := range tier.Commands {
                records = append(records, {Tier: tier.Name, Cmd: cmd, Skipped: true})
            }
            continue
        }
        for _, cmd := range tier.Commands {
            r := runOne(ctx, cmd, worktree, timeout)   // sh -c, Setpgid, tail 4KB
            r.Tier = tier.Name
            records = append(records, r)
            if r.ExitCode != 0 {
                earlyFail = true   // 不 break!本 tier 剩下的 cmd 还要跑(run-to-completion)
            }
        }
    }
    return VerifierRecord{ Commands: records, AllPassed: 全部非 Skipped 且 exit==0 }
}

// 单条命令的执行:进程组管理 + tail
func runOne(ctx, cmd, cwd, timeout) record {
    c := exec.CommandContext(timeoutCtx, "sh", "-c", cmd)
    c.Dir = cwd                                  // 这个 story 的隔离 worktree
    c.Env = os.Environ() + "CI=1"                // 让 npm/cargo 关 color、关 prompt
    c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}  // 关键!见下面
    // run, capture stdout/stderr, tail 到 4KB
    if timeout {
        kill(-pid, SIGKILL)                      // 负 PID = 整个进程组
        exitCode = 124
    }
    return record
}
```

### 信任模型(代码注释里写死的)

`cmd` 来自 operator 控制的 inbox 或 env,**当作开发者自己手敲**的 shell 输入。绝对不能把 PR 描述、网页内容这类不可信输入接进来。

### 为什么 shell 不直接 argv?

verify 命令是 story 作者写的开放输入,常出现:

```yaml
verify:
  - go test ./... && go vet ./...   # ← shell 操作符
  - pytest -k 'test_x or test_y'    # ← 引号
  - cd subdir && make               # ← 复合命令
```

不用 `sh -c` 就要自己写 shell parser。**直接复用 /bin/sh = 跟 GitHub Actions、npm scripts、Makefile 同一个选择**。

### 为什么 Setpgid + 负 PID kill?

```
exec.CommandContext("sh", "-c", "npm test")
        │ spawns
        ▼
       sh                  ← Go stdlib 只杀这个
        │ spawns
        ▼
       npm                 ← 这个不会被杀,变孤儿
        │ spawns
        ▼
       node + workers       ← 这些更不会
```

**没有 Setpgid 时**:超时只杀 sh,孙子全留下来吃 CPU、占端口、锁文件,下一次 verify 直接被它们搞死。

**有了 Setpgid**:sh 自成进程组(PGID = sh 的 PID),所有子孙默认继承。`kill(-pid, SIGKILL)` 给整个组发信号,**一次性全杀**。

### 🔍 易混点:silent on success

`Summarize()` 在 `AllPassed=true` 时**只输出一行** `verifier OK (N cmd, ms)`,不列任何 stdout。Judge prompt 里的 `formatVerifier()` 同理。

为什么?多数 round 是过的,把每条 `go test exit=0 (122ms)` 灌进 judge context 是**纯浪费 token + 分散 LLM 注意力**(judge 本来该盯 diff 跟 story,不是过的命令)。FAIL 路径才详尽。

### 🔍 易混点:Verifier 跟 Judge 的边界

verifier 的输出是 **"go test exit=1"** 这种事实陈述。
judge 的输出是 **"PASS / FAIL + 一段说明"** 这种判断。

verifier 不说"这次构建失败,我建议 worker 重写测试" —— 那是 judge 的工作。verifier 只说"这条命令 exit 非 0",由 judge 决定要不要据此 FAIL 整个 round(目前实现是必然 FAIL,见 §6 短路逻辑)。

### 🚩 踩坑记录:为什么 verifier 不用 worker 的 bash 工具

最初想:已经有 `bash` 工具了,verifier 直接调它不就行了?

不行。两者**长得像但语义不同**:

|              | worker 的 `bash` 工具      | verifier               |
| ------------ | ----------------------- | ---------------------- |
| 调用者         | LLM(决策)                | 编排代码(确定性)            |
| 输出           | truncated string 给 LLM | 完整 log 文件 + 结构化 record |
| 多命令         | LLM 自己决定调几次             | 按列表顺序跑全部              |
| 短路          | 无                       | 任何一条 fail → judge 直接 FAIL |
| 是否经过 LLM   | 是                        | 否(零 LLM 调用)            |

强行复用 = 强行套一层 agent infrastructure 来跑 `go test`,白白增加 LLM 调用、guardrails 上下文,**输出还得反向截断给非 LLM 消费**。纯亏。

### 信任模型(代码注释里写死的)

`cmd` 来自 operator 控制的 inbox 或 env,**当作开发者自己手敲**的 shell 输入。绝对不能把 PR 描述、网页内容这类不可信输入接进来。

### 为什么 shell 不直接 argv?

verify 命令是 story 作者写的开放输入,常出现:

```yaml
verify:
  - go test ./... && go vet ./...   # ← shell 操作符
  - pytest -k 'test_x or test_y'    # ← 引号
  - cd subdir && make               # ← 复合命令
```

不用 `sh -c` 就要自己写 shell parser。**直接复用 /bin/sh = 跟 GitHub Actions、npm scripts、Makefile 同一个选择**。

### 为什么 Setpgid + 负 PID kill?

```
exec.CommandContext("sh", "-c", "npm test")
        │ spawns
        ▼
       sh                  ← Go stdlib 只杀这个
        │ spawns
        ▼
       npm                 ← 这个不会被杀,变孤儿
        │ spawns
        ▼
       node + workers       ← 这些更不会
```

**没有 Setpgid 时**:超时只杀 sh,孙子全留下来吃 CPU、占端口、锁文件,下一次 verify 直接被它们搞死。

**有了 Setpgid**:sh 自成进程组(PGID = sh 的 PID),所有子孙默认继承。`kill(-pid, SIGKILL)` 给整个组发信号,**一次性全杀**。

### 🔗 跨节

- 命令来源、HERO_DEFAULT_VERIFY 见 §2 末尾"踩坑记录"
- 短路逻辑见 §6
- 行业横向对比(Aider / SWE-agent / OpenHands / Codex / Augment / Claude Code)见 `docs/verify-survey-2026.md`(若存在)

---

## 6. Judge = 短路 + LLM verdict

```
verifier.AllPassed == false?
      │
   YES│─→ 短路 FAIL,不调 LLM,直接用 verifier.Summarize 当 reason
      │
   NO ▼
collectGitContext = git log %h %s + git diff --stat + git diff
      │
      ▼
LLM.ChatWithOptions(
    msgs=[{system: judgeRole.SystemPrompt}, {user: story+verifier+gitCtx}],
    Temperature: 0.1,
    ResponseFormat: json_object,
)
      │
      ▼
parse {"verdict":"PASS|FAIL","reason":"..."}
   解析失败 → 强制 FAIL,reason="malformed: <raw>"
```

### 骨架

```go
func Run(ctx, opts) (VerdictRecord, error) {
    // 短路:verifier 跑过且红了 → 直接 FAIL,省一次 LLM 调用
    // 注意条件是 !Skipped && !AllPassed,Skipped 时(命令为空)走 LLM 路径
    if !opts.Verifier.Skipped && !opts.Verifier.AllPassed {
        return VerdictRecord{
            Verdict: "FAIL",
            ShortCircuited: true,
            Reason: verifier.Summarize(opts.Verifier),  // 失败命令 + 错误尾巴
        }
    }

    // verifier 绿了或 skipped 时,真正调 LLM 判断
    user := story + formatVerifier(v) + collectGitContext(repo, baseRef)

    // role.Model 可覆盖 provider 的默认 model,让 judge 用更强模型
    cfg := opts.Judge
    if opts.Role.Model != "" { cfg.Model = opts.Role.Model }

    resp := agent.NewLLMClient(cfg).ChatWithOptions(ctx,
        []ChatMessage{
            {Role: "system", Content: opts.Role.SystemPrompt},
            {Role: "user",   Content: user},
        },
        nil,                                            // judge 不用工具
        ChatOptions{
            Temperature:    &0.1,                       // 低温追求一致判断
            ResponseFormat: &"json_object",             // 强制结构化输出
        })

    return parseVerdict(resp)                            // 解析失败 → 强制 FAIL
}
```

### 为什么 Judge 不用工具?

Role 抽象里 `judge.AllowedTools = []`(空 slice 不是 nil,显式表达"无工具")。

给 judge 加 bash 看起来很酷("它能再跑一次测试确认!"),实际是引诱 judge 去**重新验证**而不是**判决**。两件事职能错位:

- 重新验证 = verifier 的活,deterministic、轻量、快
- 判决 = judge 的活,基于已收集的事实做 PASS/FAIL 决定

如果允许 judge 跑 bash,它会:
- 把 round 时间又拉长 30 秒
- 引入新的不确定性(LLM 用 bash 可能测出跟 verifier 不同的结果)
- 模糊 PEV 的边界,让 V1(verifier)失去权威性

### 🔍 易混点:短路条件不是"AllPassed=false"

```go
// 错的:
if !opts.Verifier.AllPassed { ... }

// 对的:
if !opts.Verifier.Skipped && !opts.Verifier.AllPassed { ... }
```

`Skipped=true` 的情况是 story 没写 `verify:` 也没设 `HERO_DEFAULT_VERIFY`。这时 `AllPassed=true`(空也是过)、`Skipped=true`。如果只看 `AllPassed`,skip 的会走短路路径直接 PASS,跳过 judge 的 LLM 判断 —— 但事实是这种情况**必须**走 LLM,因为没有任何确定性证据,只能让 judge 看 diff 决定。

### 🚩 踩坑记录:JSON 解析容忍代码块

OpenAI-compat 的 `response_format: json_object` 已经强制 LLM 输出 JSON,但有些 provider(尤其本地 fine-tune)还是会返回:

```
```json
{"verdict": "PASS", "reason": "..."}
```
```

`json.Unmarshal` 直接报 syntax error。修法是解析前先剥代码块标记:

```go
raw = strings.TrimPrefix(raw, "```json")
raw = strings.TrimPrefix(raw, "```")
raw = strings.TrimSuffix(raw, "```")
raw = strings.TrimSpace(raw)
```

**教训**:**LLM 的输出永远要假设格式偏离 schema**,即使你设了 response_format。tolerate-and-strip 比 strict-parse-and-fail 更适合生产。

### 🔗 跨节

- system prompt 在 `internal/role/role.go`,见 §2 的 Role 抽象
- verifier.Summarize 的格式见 §5

---

## 7. LLM client = 一层薄 OpenAI 兼容封装

```
cfg.BaseURL + "/chat/completions"
            │
            ▼
   POST {model, messages, tools?, temperature?, response_format?}
   Authorization: Bearer cfg.APIKey
   (InsecureTLS=true → http.Transport.TLSClientConfig.InsecureSkipVerify=true)
            │
            ▼
   两条路径:
   - Chat()   非流式:一次解 JSON,返回 ChatMessage
   - Stream() SSE:onDelta 收 text 增量,完整组装后返回
```

### 为什么有 streaming 路径(虽然 worker 用非流式)?

worker 现在用 `Chat()` 非流式,因为:
- worker 的 ReAct loop 拿到完整 ChatMessage 才能解析 tool_calls
- 流式增量对工具调用没意义(必须等结构完整才能 dispatch)

但 `Stream()` 留着是为了未来场景:
- 长 reasoning content(如 DeepSeek r1 那种)实时显示给人看
- 接 webhook / WebSocket 边生成边推送
- 如果未来加 Reviewer role 输出长 review report,流式给用户体验更好

留着没坏处,删了将来要重写。

### 🔍 易混点:`ChatMessage.Content` 字段是 `any`

```go
type ChatMessage struct {
    Role    string `json:"role"`
    Content any    `json:"content"`     // ← string 或 []ContentPart 或 ...
    ...
}
```

为什么 `any`?因为 OpenAI API 的 content 可以是:
- 单纯字符串(最常见)
- 多模态数组 `[{type:"text",...}, {type:"image_url",...}]`
- 一些 provider 的扩展形态(`[]map[string]any`)

`extractTextContent()` 函数(`agent/content.go`)就是负责把这 4 种 case 都铺平成纯文本。**读 LLM 输出永远从 extractTextContent 走,不要直接 cast `Content.(string)`**。

### 🚩 踩坑记录:env 透传给子进程的安全问题

旧的 TS 实现用 `spawn(pi, args, { env: process.env })`,把整个父进程 env 透传给 pi 子进程。问题:**JUDGE_API_KEY 也透传了**——pi 子进程根本不需要它,但 trace log 里可能漏出来。

Go 版本现在是直接的 HTTP 调用没有子进程,这个问题不存在。但提一下作为反面教材:**不要 `env: process.env`**,要 allowlist 只透传必要变量(PATH / HOME / GIT_* / SSH_* / 自己 prefix 的 PI_* 等)。

### 🔗 跨节

- InsecureTLS 详见 §2 易混点
- ChatOptions(temperature / response_format)详见 §6 judge 用法

---

## 8. Tools = OpenAI function-calling + 路径沙箱

```go
// tooldef/tool.go — 全部就这个接口
type Tool interface {
    Definition() ToolDef        // OpenAI tools[] 的 schema
    Execute(ctx, args map[string]any) (string, error)
}
```

### 沙箱(`tools/tool.go` 里的 `sandboxPath`)

```
input:
  workDir = /path/to/worktree
  path    = "src/foo.go"  或  "../escape"  或  "/etc/passwd"

      │
      ▼
1. 解析 workDir 的 symlinks → realWorkDir   ← 防 workDir 本身是 symlink
2. 把 path 算成绝对(相对则 join workDir)
3. 对结果做 partial EvalSymlinks(部分存在的目录解 symlink)
                            ↑↑↑↑↑↑↑
                            为什么要 partial?
                            write_file 要写一个还不存在的文件,
                            EvalSymlinks 会报"路径不存在"
                            partial 版只解最深存在的祖先目录
4. 检查 resolved 是否仍 in realWorkDir
   YES → ok
   NO  → SANDBOX_ESCAPE error
```

### 为什么沙箱要这么复杂?

朴素方案:`strings.HasPrefix(path, workDir)`。**漏洞百出**:

| 攻击              | 朴素方案表现             | sandboxPath 表现 |
| --------------- | ------------------ | -------------- |
| `../../etc/passwd` | path 不以 workDir 开头 → 拒绝(凑巧对了) | 同上,拒绝 |
| `workDir/../etc/passwd` | path 字符串以 workDir 开头 → 通过(错!) | Clean → 拒绝 |
| `workDir` 本身是 symlink 指向 `/private/var/...`,path 是 `/var/...` | 字符串前缀不匹配 → 拒绝(误杀,合法访问) | 解 symlink 后比较 → 通过 |
| 攻击者在 workDir 下放 symlink 指向 /etc | 字符串前缀检查通过 → 漏 | EvalSymlinks 后越界 → 拒绝 |

每一种都有真实 CVE 历史。`sandboxPath` 把这些一次性处理掉。

### 6 个核心工具

| 工具         | schema 入参                       | 用途                                    |
| ---------- | ------------------------------- | ------------------------------------- |
| `bash`     | `command`, `timeout?`            | 执行 shell(120s timeout, sandbox 检测绝对路径) |
| `read_file` | `path`, `start?`, `lines?`      | 分页读;读过的进 ReadTracker                  |
| `write_file` | `path`, `content`              | 写文件;**必须先 read 过**(防 LLM 凭空覆盖)        |
| `edit_file` | `path`, `old_string`, `new_string` | 精确替换;`old_string` 必须唯一匹配             |
| `grep` `find` `ls` | 符合直觉                          | 导航                                    |

**Worker 默认白名单**:`bash, read_file, write_file, edit_file, grep, ls`(`find` 故意不放,grep 通常更直接)。

### 🔍 易混点:`write_file` 的 "必须先 read" 约束

```go
// write_file.go 简化逻辑
if exists(path) && !readTracker.HasRead(path) {
    return error("must read_file before overwriting existing file")
}
```

为什么?LLM **猜测整个文件内容**然后 write 是 hallucination 高发点。强制它先 read 一遍,等于强制它基于真实内容做 patch,而不是凭空生成。

新文件不受这约束(本来就读不到)。

### 🔗 跨节

- 工具白名单怎么影响 LLM 的 schema 见 §4 schema 层过滤
- Tool interface 的设计源头见 Alice 项目(用户自己写的)

---

## 9. Watcher = goroutine + 信号量

### 这一节在解决什么问题

Inbox 里**同时**可能出现:
- 你 startup 之前就放进去的 5 个 story(backlog)
- 你跑着的时候用 `cp` 又新增的 3 个(live)

要求:
1. 每个 story 都被处理一次,不漏不重
2. **不能两个 goroutine 同时跑同一个 story**(会撞 worktree、撞 state 文件)
3. **总并发不能超过 `MaxParallel`**(LLM API 配额、机器资源限制)

需要两个**独立**的并发原语来解决这两个不同的问题 —— 这是这节最容易混的地方。

### 两个原语,各管一件事

```
┌─────────────────────────────────────────────────────────────┐
│  inflight: sync.Map                                         │
│      问题: "这个 story 是不是已经在跑了?"                       │
│      key:   absolute path of story.md                       │
│      value: struct{} (只用作存在标记)                          │
│      作用: 同 story 来第二次时直接 return,避免重复 dispatch     │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│  sem: chan struct{} (buffer = MaxParallel)                  │
│      问题: "全局还有空 slot 吗?"                              │
│      容量: MaxParallel(默认 2)                              │
│      作用: 第 3 个 story 来时阻塞,等前面有 goroutine 完成        │
└─────────────────────────────────────────────────────────────┘
```

### 一个 story 走完整条路径

```
1. fsnotify 报 "inbox/us-007.md Created"
   或 startup 时 readDir 扫到这个文件
                    │
                    ▼
2. dispatch("/abs/.../inbox/us-007.md") 被调用
                    │
                    ▼
3. inflight.LoadOrStore(path, struct{}{})
   "如果 key 不存在就 store,返回 (zero, false)"
   "如果已存在就什么都不动,返回 (oldValue, true)"
                    │
            ┌───────┴───────┐
       loaded=true      loaded=false
       (重复触发)       (新文件)
            │               │
          return            ▼
                    4. spawn 一个 goroutine
                            │
                            ▼
                    5. sem <- struct{}{}    ← 写入 channel
                       channel 满了就阻塞
                       (其他 goroutine 还在跑 RunOnce 时)
                            │
                            ▼ (拿到 slot)
                    6. d.RunOnce(ctx, path)  ← 真正干活,可能 1-5 分钟
                            │
                            ▼
                    7. <-sem                ← 读出,释放 slot
                            │
                            ▼
                    8. inflight.Delete(path) ← 允许这个文件再次被 dispatch
                                              (实际不会,因为已经移到 done/)
```

### 骨架(去掉错误处理)

```go
sem := make(chan struct{}, MaxParallel)  // 容量 = 上限,buffer 满即阻塞
var inflight sync.Map                     // path → struct{} 占位

dispatch := func(path string) {
    if !strings.HasSuffix(path, ".md") { return }
    abs, _ := filepath.Abs(path)

    // LoadOrStore 是 sync.Map 的原子操作:看一眼+写入是一个不可分割的步骤。
    // 两个 goroutine 同时进来,只有一个会拿到 loaded=false。
    if _, loaded := inflight.LoadOrStore(abs, struct{}{}); loaded {
        return                            // 已经有人在跑,我退出
    }

    go func() {
        defer inflight.Delete(abs)

        sem <- struct{}{}                 // 申请 slot (acquire)
        defer func() { <-sem }()          // 函数返回时释放 slot (release)

        d.RunOnce(ctx, abs)                // 真正跑 PEV round loop
    }()
}

// (1) 启动时扫 backlog
for _, f := range readDir(inbox) { dispatch(filepath.Join(inbox, f.Name())) }

// (2) 之后听 fsnotify
watcher.Add(inbox)
for ev := range watcher.Events {
    if ev.Op&fsnotify.Create != 0 { dispatch(ev.Name) }
}
```

### `chan struct{}` 当信号量 —— Go 习语

这是 Go 里**最常见的并发上限模式**,值得单独看明白。

**关键事实**:Go 的 buffered channel
- `make(chan T, N)`:容量 N 的 channel
- `ch <- x`:塞东西。**buffer 满时阻塞**,直到有人取
- `<-ch`:取东西。buffer 空时阻塞,直到有人塞

**用作信号量**:
```go
sem := make(chan struct{}, 2)    // 最多 2 个 slot
```

| 当前 channel buffer | 新来 `sem<-` 行为   |
| ----------------- | --------------- |
| 空 [_, _]           | 立刻成功,变 [x, _] |
| 半满 [x, _]         | 立刻成功,变 [x, x] |
| 满 [x, x]           | **阻塞**,等到有人 `<-sem` |

每个想干活的 goroutine 先 `sem <- struct{}{}`(acquire,占 slot),干完 `<-sem`(release,腾 slot)。第三个来的会阻塞在 send 上,直到前两个里有一个完成 release。

**`struct{}` 是空类型,占 0 字节** —— 不传任何数据,纯粹是"令牌"。等价于 Java 的 `Semaphore` 或 Python 的 `asyncio.Semaphore`,但 Go 直接用 channel 的阻塞语义实现,不需要专门的库。

### `sync.Map.LoadOrStore` —— 原子合一

朴素方式有 race:

```go
// 错误!两个 goroutine 同时进来都会 dispatch
if _, exists := m[path]; !exists {
    m[path] = struct{}{}     // ← 两个都到这一步,不互斥
    spawnWork(path)
}
```

`sync.Map.LoadOrStore(key, value)` 把 "查 + 没有就写" 做成**单步原子**:
```
返回 (existing, true)  -- key 已存在,什么都不变
返回 (your value, false) -- key 不存在,刚刚写进去了
```

两个 goroutine 同时调,**保证只有一个拿到 false**。我们看到 false 就 dispatch,看到 true 就退出。这是为什么不需要 mutex。

### 关键不变量

- ✅ 同一 story 不被并行处理(`inflight`)
- ✅ 全局并发 ≤ `MaxParallel`(`sem` 容量)
- ✅ 单 story 内串行(`RunOnce` 是同步函数,goroutine 内只调一次)—— 跟 Cognition "single-threaded writes" 原则一致

### 🔍 易混点:`inflight` 是按 path 锁,不是按 storyId

如果你把 inbox 同一个 story 文件**改个名再 cp 进去**(`cp us-001.md us-001-v2.md`),它们的 path 不同,`inflight` 不会判重,会同时被 dispatch。

但因为 `RunOnce` 内部按 `storyId`(frontmatter id 字段)算 worktree 路径和 state 文件,**第二个会撞第一个的 worktree**,出问题。

实操中不会撞,因为 story 作者不会这么干。但严格说,我们的 inflight key 选 path 是个**便利但不严格**的选择。如果未来有"批量重命名 inbox 文件"的场景,要换成按 `storyId` 锁。

### 🚩 踩坑记录:Semaphore 的 release 顺序

我**自己写过一个**Semaphore class(struct + active 计数 + waiter 列表),长这样:

```go
// 第一版:有 race
acquire():
    if active < max { active++; return }
    wait()       // 把自己塞进 waiters,挂起
    active++     // ← 在 release 把我唤醒后再 ++

release():
    active--           // 先减
    notifyOneWaiter()  // 后唤醒
```

**race 在哪**:`release()` 第一行 `active--`(从 max 变成 max-1),还没来得及唤醒 waiter,这时一个**新的** `acquire()` 进来:
- 看到 `active < max` → 直接 `active++`(从 max-1 变 max),拿到 slot
- 然后 `release` 那边唤醒 waiter,waiter 又 `active++` → **变成 max+1,超发**

Node.js 单线程跑不会撞(整个流程在同一个 microtask 内),但 Go 多 goroutine **必撞**。

**修法**:slot 在 release 时**不减**,而是直接交给 waiter("hand-off"):

```go
release():
    if next := waiters.shift(); next != nil {
        next()           // 我的 slot 直接给下一个,active 不变
        return
    }
    active--             // 没人等,才真正释放
```

详见 commit `7704050` 的 Semaphore 节。

**最终选 channel 方案的理由**:就是为了避开这种 race。`make(chan, N)` 的 buffer 满/空语义是 runtime 用 hchan 锁保证的,你不可能写错。**能用 channel 表达的并发,优先 channel,不要自己写计数器**。

### 🚩 踩坑记录:Semaphore 的 release 顺序

最初的 Semaphore 实现:

```go
// 旧版(有 race)
acquire():
    if active < max { active++; return }
    wait()       // 在 channel 上等
    active++     // ← 在 release() 信号到达后增加

release():
    active--           // ← 这里
    notifyOneWaiter()  // ← 然后唤醒 waiter
```

**race**:`release` 把 `active` 减成 max-1,然后唤醒 waiter。Waiter 还没醒来时,新来的 `acquire()` 看到 `active < max` → 不等待,直接 `active++` 拿到 slot。等 waiter 真的醒来再 `active++`,**就 max+1 了,超发**。

Node.js 单线程下凑巧不会撞,但 Go 多 goroutine 必出问题。

**修法**:slot 在 release 时**不减**,而是直接交给 waiter:

```go
release():
    if next := waiters.shift(); next != nil {
        next()           // 把我手里的令牌直接转手,active 不变
        return
    }
    active--             // 没人等,才真正释放
```

详见 commit `7704050` 的 Semaphore 节。

### 🔗 跨节

- `RunOnce` 内部串行执行的 round loop 见 §3
- 整个并发模型上层观点(per-story 串行,inter-story 并行)= 调研 §B.7 反对的 "parallel-writer swarm"

---

## 总结图(一张图记住整个项目)

```
                    ┌─────────────────────────────────────┐
                    │  user ⇄ Claude Code/Codex (Plan)    │
                    └────────────────┬────────────────────┘
                                     │ story.md
                                     ▼
                              ┌──────────────┐
                              │   inbox/     │
                              └──────┬───────┘
                                     │ fsnotify
                ┌────────────────────▼────────────────────┐
                │      Dispatcher (semaphore N)           │
                │  ┌──────────────────────────────────┐   │
                │  │ git worktree add hero/<id>       │   │
                │  │ state.Load(); resume? validate   │   │
                │  └──────────────┬───────────────────┘   │
                │                 │                       │
                │  ┌──────────────▼───────────────────┐   │
                │  │  for round in 1..limit:          │   │
                │  │  ┌─────────┐  ┌──────────┐       │   │
                │  │  │ Worker  │→ │ rescue   │       │   │
                │  │  │  ReAct  │  │ commit   │       │   │
                │  │  │  loop   │  └────┬─────┘       │   │
                │  │  └─────────┘       ▼             │   │
                │  │             ┌──────────┐         │   │
                │  │             │ Verifier │         │   │
                │  │             │  shell   │         │   │
                │  │             └────┬─────┘         │   │
                │  │                  ▼               │   │
                │  │             ┌──────────┐         │   │
                │  │             │  Judge   │         │   │
                │  │             │ short-or │         │   │
                │  │             │   LLM    │         │   │
                │  │             └────┬─────┘         │   │
                │  │       PASS ◄─────┴─────► FAIL    │   │
                │  └─────────┬─────────────┬──────────┘   │
                │            │             │              │
                │   move to done/    appendToStory        │
                │                    next round           │
                │            │                            │
                │  defer: cleanupWorktree if !running     │
                └────────────────┬────────────────────────┘
                                 │
                                 ▼
                       state.Write (atomic JSON)
                       runs/state/<id>.json
                       runs/<id>-<ts>.json (final)
                       runs/traces/<id>-r<n>-*.jsonl (per-round)
```

---

## 复习自测题(读完应能答)

如果以下问题答不上来,回到对应章节再读一遍。

1. **§1**:`Stats.FinalStatus` 三种取值各代表什么时刻?哪一种触发 resume?
2. **§2**:为什么 InsecureTLS 是 provider 维度而不是全局?把它做成全局会出什么事?
3. **§3**:`autoRescueCommit` 必须放在 worker 跟 verifier 之间,放别处会怎样?
4. **§3**:resume 路径开头的 `git rev-parse` 校验,如果省略,在什么场景会崩?
5. **§4**:Schema 层限工具 vs Runtime 层限工具,差在哪里?为什么前者更好?
6. **§4**:`loop` guardrail 的 sig 用 `name+args` 而不是 `name`,差别是什么?
7. **§5**:Setpgid + `kill(-pid)` 解决了什么问题?不加会发生什么?
8. **§6**:Judge 的短路条件为什么是 `!Skipped && !AllPassed` 不是 `!AllPassed`?
9. **§7**:`ChatMessage.Content` 为什么是 `any` 而不是 `string`?
10. **§8**:`write_file` 为什么要求"必须先 read"?新文件为什么不受此约束?
11. **§9**:Semaphore 的 release 为什么是"hand-off 给 waiter",而不是"active-- 然后唤醒"?

---

## 词汇表

| 术语                        | 含义                                                         |
| ------------------------- | ---------------------------------------------------------- |
| **PEV**                   | Plan-Execute-Verify;hero-coding 是其简化版,Plan 在外              |
| **ReAct**                 | LLM 边推理边调工具的 loop 模式(reasoning + acting 交替)                |
| **Role**                  | 一个 LLM 配置 slot:system prompt + allowed tools + 可选 model 覆盖 |
| **Provider**              | 一个 LLM endpoint:BaseURL + APIKey + 可选 InsecureTLS          |
| **Binding**               | `<provider>/<model>` 短字符串,把 role 绑到具体连接                    |
| **Schema 层限工具**           | 让被禁工具不出现在 LLM 的 tools[] 列表里(物理隔离)                          |
| **Runtime guardrail**     | 工具调用执行前拦截的兜底(loop / cap / violation)                       |
| **Defense-in-depth**      | 4 层独立防御:prompt / schema / runtime / tool sandbox           |
| **autoRescueCommit**      | Dispatcher 在 worker 结束后兜底 commit dirty file 的机制            |
| **Setpgid**               | Unix 系统调用,把进程放入新进程组,便于 kill 整组                             |
| **negative PID kill**     | `kill(-pid, signal)` 给整个进程组发信号                             |
| **resume**                | 从 `FinalStatus="running"` 的 state 文件继续上次跑到一半的 round        |
| **short-circuit (judge)** | verifier 红灯时跳过 LLM 直接 FAIL                                 |
| **filesystem-as-context** | 用通用 bash/read/edit/grep 五件套,而非特化 ACI                       |

---

## 相关文档

- `architecture-research-2026.md` — 这个项目为什么是这种形状(2026 SOTA harness 调研)
- 根目录 `README.md` / `README.zh.md` — 用户视角的使用文档
