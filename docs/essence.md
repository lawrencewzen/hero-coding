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

### 🚩 踩坑记录:resume 时 baseSha 死链

**bug 复现**:
1. 第一次跑 us-001,coproxy 断了,worker 中途崩,state 留在 `running`,记录的 baseSha 是 `ff4d25b...`
2. 我手动跑 `setup-target.sh`,`rm -rf .git && git init` 重置 target-repo
3. 重新跑 hero,它检测到 `state.FinalStatus == "running"` → 走 resume 分支
4. 拿 baseSha `ff4d25b...` 喂给 `git worktree add`
5. **fatal: invalid reference: ff4d25b...**(因为新的 .git 完全不知道这个 commit)

**修法**(commit `98a9a0c`):resume 路径开头加一行 `git rev-parse --verify <baseSha>^{commit}`,失败就 clear state、当作首次跑:

```go
if resume {
    if _, err := git("rev-parse", "--verify", prior.BaseSha+"^{commit}"); err != nil {
        d.log.Warn("recorded baseSha no longer exists, falling back to fresh start")
        state.Clear(...); prior = nil; resume = false
    }
}
```

**教训**:**resume 不能 trust state**,必须验外部世界(git repo 状态)是否还跟 state 记录一致。类似的还有"worktree 目录是否还在"(已有的 `isWorktreeOnBranch` 检查)。

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

| 层           | 机制                                          | 失败模式             |
| ----------- | ------------------------------------------- | ---------------- |
| **Prompt**  | system prompt 写"你只能用 X, Y, Z"               | 模型可能不遵循         |
| **Schema**  | LLM 看到的 `tools[]` 只含 allowed —— **物理上不存在被禁工具** | 模型即使想用也调不出      |
| **Runtime** | `guardrails.observe()` 调用前拦截               | 兜底 schema 漏网 / 调用循环 / 总数超限 |
| **Tool**    | `sandboxPath` 限制路径在 worktree 内              | 路径穿越 / symlink 逃逸 |

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

## 5. Verifier = 确定性的 shell runner

```go
func Run(ctx, opts) (VerifierRecord, error) {
    // story.verify 优先;没写的话回落到 HERO_DEFAULT_VERIFY
    cmds := opts.Story.Verify or opts.DefaultCommands
    if len(cmds) == 0 { return Skipped }    // skip 不算失败,默认 PASS

    for _, cmd := range cmds {
        // 关键:Setpgid → 把 sh 放进自己的进程组
        // 否则超时只能杀直接子进程 sh,孙子(npm/node/...)成孤儿不死
        c := exec.CommandContext(timeoutCtx, "sh", "-c", cmd)
        c.Dir = worktree                    // cwd = 这个 story 的隔离 worktree
        c.Env = os.Environ() + "CI=1"       // 让 npm/cargo 等关掉颜色和 prompt
        c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
        // ... run, capture stdout/stderr, tail 到 4KB
        if timeout {
            kill(-pid, SIGKILL)             // 负 PID = 给整个进程组发信号
            exitCode = 124
        }
    }

    return VerifierRecord{
        AllPassed: 所有 exit==0,
        Commands:  []CommandRecord,         // 每条命令的 exit / 时长 / tail 输出
        LogFile:   写完整输出到 runs/...-verify-r<n>.log,  // 给人看不给 LLM
    }
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

### 🔍 易混点:Verifier 跟 Judge 的边界

verifier 的输出是 **"go test exit=1"** 这种事实陈述。
judge 的输出是 **"PASS / FAIL + 一段说明"** 这种判断。

verifier 不说"这次构建失败,我建议 worker 重写测试" —— 那是 judge 的工作。verifier 只说"这条命令 exit 非 0",由 judge 决定要不要据此 FAIL 整个 round(目前实现是必然 FAIL,见 §6 短路逻辑)。

### 🔗 跨节

- 命令来源、HERO_DEFAULT_VERIFY 见 §2 末尾"踩坑记录"
- 短路逻辑见 §6

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

```go
// dispatcher/watch.go — 用 channel 当 semaphore
sem := make(chan struct{}, MaxParallel)  // buffer 大小 = 并发上限
inflight := sync.Map{}                    // 防同一文件被 dispatch 两次

dispatch := func(path) {
    if inflight.LoadOrStore(path) {
        return                            // 已在跑 / 已 enqueue,跳过
    }
    go func() {
        sem <- struct{}{}                 // 拿令牌(满了会阻塞)
        defer func() { <-sem }()          // 还令牌
        d.RunOnce(ctx, path)
        inflight.Delete(path)             // RunOnce 结束后才删,允许重新 dispatch
    }()
}

// 1. 启动时扫一遍 inbox(可能有未处理的 backlog)
for _, f := range readDir(inbox) { dispatch(f) }

// 2. 之后用 fsnotify 听新事件
watcher.On("Create", dispatch)
```

### 关键不变量

- **同一 story 不会被并行处理**(`inflight` 锁,基于 absolute path)
- **全局并发上限 = `MaxParallel`**(`sem` channel buffer)
- **单 story 内串行**(`RunOnce` 自己是同步函数)—— 跟 Cognition "single-threaded writes" 原则一致

### 🔍 易混点:为什么不用 mutex?

`sync.Map.LoadOrStore` 是原子的,本身就避免了 race。如果用 `Map[path] && Map[path]=true` 两步,两个 goroutine 可能都看到不存在然后都 store。

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

| 术语                          | 含义                                                            |
| --------------------------- | ------------------------------------------------------------- |
| **PEV**                     | Plan-Execute-Verify;hero-coding 是其简化版,Plan 在外           |
| **ReAct**                   | LLM 边推理边调工具的 loop 模式(reasoning + acting 交替)              |
| **Role**                    | 一个 LLM 配置 slot:system prompt + allowed tools + 可选 model 覆盖 |
| **Provider**                | 一个 LLM endpoint:BaseURL + APIKey + 可选 InsecureTLS         |
| **Binding**                 | `<provider>/<model>` 短字符串,把 role 绑到具体连接                    |
| **Schema 层限工具**           | 让被禁工具不出现在 LLM 的 tools[] 列表里(物理隔离)                       |
| **Runtime guardrail**       | 工具调用执行前拦截的兜底(loop / cap / violation)                        |
| **Defense-in-depth**        | 4 层独立防御:prompt / schema / runtime / tool sandbox            |
| **autoRescueCommit**        | Dispatcher 在 worker 结束后兜底 commit dirty file 的机制             |
| **Setpgid**                 | Unix 系统调用,把进程放入新进程组,便于 kill 整组                        |
| **negative PID kill**       | `kill(-pid, signal)` 给整个进程组发信号                              |
| **resume**                  | 从 `FinalStatus="running"` 的 state 文件继续上次跑到一半的 round        |
| **short-circuit (judge)**   | verifier 红灯时跳过 LLM 直接 FAIL                                    |
| **filesystem-as-context**   | 用通用 bash/read/edit/grep 五件套,而非特化 ACI                        |

---

## 相关文档

- `architecture-research-2026.md` — 这个项目为什么是这种形状(2026 SOTA harness 调研)
- 根目录 `README.md` / `README.zh.md` — 用户视角的使用文档
