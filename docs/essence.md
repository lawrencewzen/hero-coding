# hero-coding 精读

按读懂顺序排,每节"做什么 + 关键流程 + 最简骨架代码(去掉边界处理和日志)"。完整 ~6300 行的项目压成下面这一份。

适合 4 类场景:
1. 想快速 onboarding 整个项目
2. 改代码前定位职责边界
3. 给别人做技术分享前对账
4. 一段时间没碰回来找回手感

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
│   Worker                │  ◄── PEV 三阶段
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

| 模块 | 职责 | 关键文件 |
|---|---|---|
| `cmd/hero` | CLI 入口,signal 处理 | `main.go` |
| `dispatcher` | 编排 + worktree + watcher | `dispatcher.go` `git.go` `watch.go` |
| `worker` | ReAct + schema 限工具 + guardrails | `worker.go` `guardrails.go` |
| `verifier` | 跑 shell 命令拿确定性证据 | `verifier.go` |
| `judge` | 短路或 LLM 判 PASS/FAIL | `judge.go` |
| `agent` | OpenAI-compat HTTP client | `llm.go` |
| `tools` | bash/read/write/edit/grep/ls(沙箱) | `tools/*.go` |
| `state` `story` `role` `config` `provider` | 数据/配置 | 各自一两个小文件 |

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
    StoryID, Branch, BaseSha, WorktreePath string
    WorkerRuns    []WorkerRunStats
    Verifications []VerifierRecord
    Verdicts      []VerdictRecord
    FinalStatus   string // "done" | "gave_up" | "running"
}
```

**心智模型**:`Stats` 是事实表,3 个 `[]record` 是按 round 累积的事件;`FinalStatus="running"` 是 resume 的唯一信号。

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

| 切换需求 | 改动 |
|---|---|
| 换 worker 模型 | `HERO_WORKER=coproxy/claude-4.7` |
| 换 provider | `HERO_WORKER=openai/gpt-5.5` |
| 加新 endpoint | 加 3 行 `HERO_PROVIDER_<name>_*` |
| 一次性覆盖 | `HERO_WORKER=... ./hero run ...` |

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
       │     NO  → clear stale state
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

### 骨架

```go
func (d *Dispatcher) RunOnce(ctx, storyPath) (*Stats, error) {
    s := story.Parse(...)
    prior := state.Load(...)
    resume := prior.FinalStatus == "running"

    if resume && !rev_parse(prior.BaseSha) { // 关键:防 setup-target 之后死链
        state.Clear(...); resume = false
    }

    worktree, baseSha := prepare or restoreFromPrior(...)
    stats := initOrResume(prior, ...)
    defer cleanupIfTerminal()  // FinalStatus != "running" 才清

    for round := startRound; round <= limit; round++ {
        w := d.worker.Run(...)
        autoRescueCommit(worktree, scope)            // <- defense
        v := verifier.Run(...)
        j := judge.Run(..., verifier=v)
        if j == FAIL { feedback = j.reason + verifier.Summarize(v); appendToStory(round, feedback) }
        state.Write(stats)
        if j == PASS { stats.FinalStatus = "done"; break }
    }
    if stats.FinalStatus == "running" { stats.FinalStatus = "gave_up" }
    state.WriteRun(...)
    if PASS { mv inbox/ → done/ }
}
```

**两个非显然之处**:
- `defer cleanupWorktree` 的判定是 `FinalStatus != "running"` —— 只有"中途崩"才保留 worktree 给下次 resume。
- `autoRescueCommit` 在 worker 跟 verifier 之间,因为 LLM 经常忘 git commit,verifier 跑前必须确保改动已落 commit,否则 judge 看不到。

---

## 4. Worker = 自己写的 ReAct loop

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
│    if guard.observe(tc) != "" → kill round (loop/cap/violation)
│    result := tools[tc.Name].Execute(tc.args)             │
│    msgs += {role:"tool", tool_call_id:tc.id, content:r}  │
│                                                          │
└──────────────────────────────────────────────────────────┘
```

### 骨架

```go
func (w *Worker) Run(ctx, opts) (Stats, error) {
    allowed := w.role.AllowedTools

    // === schema 层限工具:不 allowed 的工具不进 toolDefs ===
    tools := buildTools(workDir)
    toolDefs := []map[string]any{}
    for _, t := range tools {
        if _, ok := allowSet[t.Name]; ok { toolDefs = append(toolDefs, t.Definition()) }
    }

    msgs := []ChatMessage{
        {Role: "system", Content: composeSystemPrompt(w.role.SystemPrompt, allowed)},
        {Role: "user", Content: storyBody + maybeFeedback},
    }
    guard := newGuardrails(allowed)

    wallCtx, cancel := WithTimeout(ctx, MaxWallTime)  // 5 min
    defer cancel()

    for iter := 0; iter < MaxIterations; iter++ {
        resp := w.llm.Chat(wallCtx, msgs, toolDefs)
        msgs = append(msgs, resp)
        if len(resp.ToolCalls) == 0 { break }
        for _, tc := range resp.ToolCalls {
            if kill := guard.observe(tc); kill != "" { stats.KillReason = kill; return }
            result := toolByName[tc.Name].Execute(tc.args)
            msgs = append(msgs, {Role:"tool", ToolCallID: tc.ID, Content: result})
        }
    }
}
```

### Defense-in-depth(关键概念,记住这个表)

| 层 | 机制 | 失败模式 |
|---|---|---|
| **Prompt** | system prompt 写"你只能用 X, Y, Z" | 模型可能不遵循 |
| **Schema** | LLM 看到的 `tools[]` 只含 allowed —— **物理上不存在被禁工具** | 模型即使想用也调不出 |
| **Runtime** | `guardrails.observe()` 调用前拦截 | 兜底 schema 漏网 / 调用循环 / 总数超限 |
| **Tool** | `sandboxPath` 限制路径在 worktree 内 | 路径穿越 / symlink 逃逸 |

### Guardrails 三种 kill

| 名字 | 触发条件 | 数字 |
|---|---|---|
| `tool_cap` | 单轮工具调用 > 80 | 80 |
| `wall_timeout` | 单轮墙时间 > 5min | 通过 `ctx WithTimeout` |
| `loop` | 同 `name+args` 在最近 6 次调用里出现 ≥ 4 次 | window=6, threshold=4 |
| `tool_violation` | 工具名不在 allowed 里(schema 失守的兜底) | — |

---

## 5. Verifier = 确定性的 shell runner

```go
func Run(ctx, opts) (VerifierRecord, error) {
    cmds := opts.Story.Verify or opts.DefaultCommands
    if len(cmds) == 0 { return Skipped }

    for _, cmd := range cmds {
        // 关键:Setpgid → 超时杀整个进程组,不留孤儿
        c := exec.CommandContext(timeoutCtx, "sh", "-c", cmd)
        c.Dir = worktree
        c.Env = os.Environ() + "CI=1"
        c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
        // ... run, capture stdout/stderr, tail 到 4KB
        if timeout { kill(-pid, SIGKILL); exitCode = 124 }
    }

    return VerifierRecord{
        AllPassed: 所有 exit==0,
        Commands:  []CommandRecord,
        LogFile:   写完整输出到 runs/...-verify-r<n>.log,
    }
}
```

**信任模型**(代码注释里写死的):`cmd` 来自 operator 控制的 inbox 或 env,**当作开发者自己手敲**的 shell 输入。绝对不能把 PR 描述、网页内容这类不可信输入接进来。

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
    if !opts.Verifier.Skipped && !opts.Verifier.AllPassed {
        return VerdictRecord{ Verdict: "FAIL", ShortCircuited: true,
                              Reason: verifier.Summarize(opts.Verifier) }
    }

    user := story + formatVerifier(v) + collectGitContext(repo, baseRef)
    cfg := opts.Judge; cfg.Model = opts.Role.Model || cfg.Model

    resp := agent.NewLLMClient(cfg).ChatWithOptions(ctx,
        []ChatMessage{{system: opts.Role.SystemPrompt}, {user: user}},
        nil,  // judge 不用工具
        ChatOptions{Temperature: &0.1, ResponseFormat: &"json_object"})

    return parseVerdict(resp)  // PASS|FAIL + reason
}
```

**为什么 Judge 不用工具**:Role 抽象里 `judge.AllowedTools = []`(空 slice 不是 nil,显式表达"无工具")。Judge 唯一的产物是 JSON verdict;给它 bash 反而是引诱它去验证而非判决。

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

InsecureTLS 是 **provider 维度**的:每个 client 拿到自己 transport 的 clone,改 TLSClientConfig,所以一个野 endpoint 不会污染其它 client。

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
1. 解析 workDir 的 symlinks → realWorkDir
2. 把 path 算成绝对(相对则 join workDir)
3. 对结果做 partial EvalSymlinks(部分存在的目录解 symlink)
4. 检查 resolved 是否仍 in realWorkDir
   YES → ok
   NO  → SANDBOX_ESCAPE error
```

工具调用前都经这个守卫,所以即便 LLM 想 `read_file("/etc/passwd")` 也会被拒。

### 6 个核心工具

| 工具 | schema 入参 | 用途 |
|---|---|---|
| `bash` | `command`, `timeout?` | 执行 shell(120s timeout, sandbox 检测绝对路径) |
| `read_file` | `path`, `start?`, `lines?` | 分页读;读过的进 ReadTracker |
| `write_file` | `path`, `content` | 写文件;**必须先 read 过**(防 LLM 凭空写覆盖) |
| `edit_file` | `path`, `old_string`, `new_string` | 精确替换;`old_string` 必须唯一匹配 |
| `grep` `find` `ls` | 符合直觉 | 导航 |

**Worker 默认白名单**:`bash, read_file, write_file, edit_file, grep, ls`(`find` 故意不放,grep 通常更直接)。

---

## 9. Watcher = goroutine + 信号量

```go
// dispatcher/watch.go — 用 channel 当 semaphore
sem := make(chan struct{}, MaxParallel)
inflight := sync.Map{}  // 防同一文件被 dispatch 两次

dispatch := func(path) {
    if inflight.LoadOrStore(path) { return }  // 已在跑
    go func() {
        sem <- struct{}{}; defer func() { <-sem }()
        d.RunOnce(ctx, path)
        inflight.Delete(path)
    }()
}

// backlog
for _, f := range readDir(inbox) { dispatch(f) }

// fsnotify
watcher.On("Create", dispatch)
```

**关键不变量**:
- 同一 story 不会被并行处理(`inflight` 锁)
- 全局并发上限 = `MaxParallel`(`sem` channel buffer)
- 单 story 内串行(`RunOnce` 自己是同步函数)—— 跟 Cognition "single-threaded writes" 原则一致

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

## 附:相关文档

- `architecture-research-2026.md` — 这个项目为什么是这种形状(2026 SOTA harness 调研)
- 根目录 `README.md` / `README.zh.md` — 用户视角的使用文档
