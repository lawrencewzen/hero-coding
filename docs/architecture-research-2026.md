# 全自动化 Coding 系统架构调研（2026-05）

调研对象：现有 SOTA 全自动化 coding agent 系统的架构。
目的：为 hero-coding 项目的下一阶段架构演进定锚点。
调研时间：2026-05-06。

---

## TL;DR

1. 2026 年 SOTA harness 已经收敛到一个可识别的形状：**单一参数化 Agent + 多 Role + Plan-Execute-Verify (PEV) + git worktree 沙箱 + 事件日志**。
2. 模型架构（scaffolding）相对于裸模型仍贡献 5-15 个百分点 SWE-bench Verified 分数 —— harness 不是花架子。
3. 但 OpenAI Codex 团队 2026 年提出的反向思潮："Scaffolding is coping, not scaling" —— 不要为模型 6 个月后会内化的能力堆复杂逻辑。
4. **多 agent 并行写代码** 已被 Cognition 实测否定；**多 agent 加智能（reviewer / router / verifier）** 被普遍接受。
5. **Verifier ≠ Judge**：Verifier 跑确定性证据（测试、lint、type check）；Judge 是 LLM 用 verifier 输出 + diff 做最终判断。这是 2026 年成型的 PEV 模式。
6. **filesystem-as-context** 击败了"为 agent 设计特化 ACI"——bash/read/grep/edit/write 五件套加文件系统胜过 100+ 特化工具（Microsoft Azure SRE 数据：45% → 75%）。
7. 对 hero-coding 的具体路线：**先做 Verifier 阶段 → 再做 Agent+Role 重构 → 顺手做 schema-level tool restriction**，其余先不动。

---

## A. 2026 SOTA 架构的标准形状

把 SWE-bench 头部方案（May 2026 头部 88-94%）、Devin、OpenHands V1、Claude Code、SWE-agent、Augment 这些放一起看，骨架已经收敛：

```
┌─────────────── Story / Issue / Spec ───────────────┐
                          │
         ┌────────────────┼────────────────┐
         ▼                ▼                ▼
   [Codebase Index]  [Sandbox]      [Persistent Log]
   wiki / vector     worktree /     event-sourced
   / git+grep        docker         (replayable)
                          │
                          ▼
              ┌─────── Agent Loop ───────┐
              │  ReAct (think → tool →   │
              │  observe → think...)     │
              │  with bounded ACI tools  │
              └──────────┬───────────────┘
                         │
                ┌────────┼────────┐
                ▼                 ▼
         [Verifier]         [Reviewer]
         真实测试 / lint    第二个 fresh-context
         100%-precision     agent，看 PR 找 bug
                │                 │
                └────────┬────────┘
                         ▼
                    PASS / FAIL
                         │
              ┌──────────┴──────────┐
            PASS                   FAIL
              │                     │
              ▼                     ▼
        commit / PR          structured feedback
                             → next round
```

### 载重组件（几乎所有 SOTA 都有）

1. **Sandbox = git worktree 或 container**。OpenHands V1 把它做成可选的 workspace 抽象，本地和远程同 API。
2. **ACI（Agent-Computer Interface）** —— 2024-2025 主流是为 agent 设计特化工具；**2026 反向**了，filesystem + 标准 bash/read/edit/write 更好（Azure SRE 数据）。
3. **Verifier ≠ Judge**：Verifier 跑确定性证据（测试、lint、type check）；Judge 是 LLM 用 verifier 输出 + diff + 读代码做最终判断。这两件事拆开是过去一年的明显趋势，2026 年正式命名为 PEV (Plan-Execute-Verify)。
4. **事件日志 = 状态**。OpenHands V1 用 append-only EventLog 做 single source of truth，崩溃后从头重放就恢复。
5. **Reviewer pattern**。Cognition 实测："fresh-context reviewer catches ~2 bugs per PR, 58% severe"。新鲜上下文比同一 agent 自己 review 更有效，因为没被 coder 的 implicit bias 污染。

### 70 个真实 harness 的分布（April 2026 调查）

| 子 agent 模式 | 占比 |
|---|---|
| 单 agent | **30%**（最大派） |
| Orchestrator-worker | 18.6% |
| 工具委托 | 17.1% |
| 多层递归 | 12.9% |

| 隔离层级 | 占比 |
|---|---|
| Process separation | 45% |
| Container | 31% |
| 无隔离 | 17% |
| WASM | 7% |

| 工具系统 | 占比 |
|---|---|
| Registry-based | 34.3% |
| MCP-first | 14.3% |
| Plugin | 10% |
| Hard-coded | 11.4% |

---

## B. 2026 关键架构原则

### 1. "Scaffolding is coping, not scaling" —— OpenAI Codex 团队 (2026)

> *"If you rely on complex scaffolding to build AI agents you aren't scaling you are coping."*
>
> *"[Complex scaffolding] constrained it ... in a way that prevents it from expressing its true capability."*

具体撕掉了什么：
- **Context compaction 启发式逻辑** —— 之前手工写的"长会话压缩历史"代码全部删除，改为训模型让它原生跨 20+ 个 context window 不掉链子。
- 理由是 Bitter Lesson：你今天精心搭的 scaffold，下一代模型就抱歉了把它内化掉，反而成为限制。

**实战影响**：任何你正在写的复杂 harness 逻辑，问自己一句"这会不会 6 个月后被模型升级吃掉"。如果会，倾向不写。

### 2. Plan-Execute-Verify (PEV) —— 2026 命名模式

```
Plan       (high-reasoning model 拆解 + 验收标准)
  ↓
Execute    (cheap model 在受限工具下干活)
  ↓
Verify     (deterministic gate — 不是 LLM)
```

也叫 **Reasoning Sandwich**。**关键是 Verify 必须是 deterministic 的**：

> "Telling an agent 'follow our coding standards' in a prompt is fundamentally different from wiring a linter that blocks the PR when standards are violated."
>
> "Deterministic enforcement over probabilistic compliance"

具体推荐做法：
- 复杂度上限当成硬 CI 失败：`"complexity": ["error", { "max": 10 }]`、`"max-depth": ["error", 4]`
- 结构化错误信息：lint 违规必须带修复建议，不只是报告问题
- 阶段边界强制：不只是事后测试

### 3. 单一参数化 Agent 类 > 类层级（March 2026 论文）

> "We initially created separate classes for planning, code exploration, and web generation agents but found this created a diamond problem when subagents needed mixed capabilities. We replaced this with a single parameterized `MainAgent` class where behavioral variation comes from construction parameters like `allowed_tools` and system prompt overrides."

**Agent + Role 抽象是被实测验证的**。Worker / Judge / Planner 都是同一个 Agent 类 + 不同 Role 配置。

### 4. Schema-level 工具限制 > runtime 检查

同一篇论文：

> "Delegating planning to a specialized Planner subagent with **read-only tools** in its schema proved more robust. ... write tools do not exist in its schema, not because a runtime check blocks the attempt."

要约束某个 Role 不能做某事，让它在工具 schema 里直接没有那个工具，而不是 runtime guard。当 Role 是 Planner 时，它的 `allowed_tools` 里干脆没 edit/write/bash —— 它**物理上做不了**修改。

### 5. Filesystem-as-context > 特化 ACI（Microsoft Azure SRE 数据）

35,000+ 生产事件 SRE Agent 的反直觉发现：

> "Microsoft shifted from 100+ bespoke tools to a **filesystem-based context engineering system** ... exposing everything as files and letting agents use standard tools outperformed specialized tooling — Intent Met score rose from 45% to 75%."

bash + read + grep + edit + write 这套通用工具，加上把所有上下文表达为文件，比给 agent 设计 100 个特化工具更好用。这跟 SWE-agent 论文 2024 的"特化 ACI"立场是相反的 —— 行业已经反向了。

### 6. Defense-in-depth 安全（5 层）

> "Five independent safety layers: prompt-level guardrails, schema-level tool restrictions, runtime approvals, tool-level validation, user-defined lifecycle hooks. No single layer is relied upon exclusively and each catches a different failure mode."

层数：
1. **Prompt 层**：system prompt 提示规则
2. **Schema 层**：Role 工具集裁剪（Judge 不应有 git commit）
3. **Runtime 层**：approval / 守护栏（loop / wall-time）
4. **Tool 层**：单工具内的验证（linter on edit）
5. **Lifecycle 层**：pre-commit hook、SessionStart hook

### 7. 多 agent 的细分立场

| 模式 | 业内立场 |
|---|---|
| Parallel-writer swarm | **不要做** —— Cognition Flappy Bird 案例：implicit assumption 撞车 |
| Reviewer / Verifier / Router 加智能 | **要做** —— Cognition 数据：~2 bugs/PR, 58% severe |
| Hierarchical orchestration | 复杂任务可以，但要 single-threaded writes |
| Weaker-to-stronger model 路由 | 还是 open problem，2026 暂不可靠 |
| Same-tier model 路由（Claude+GPT） | 可以，作为"capability router" |
| 多层 unstructured agent network | "mostly a distraction" |

### 8. SWE-agent ACI 经典原则（2024，仍部分有效）

- 文件查看器一次只给 100 行，不给整个文件
- 搜索结果只返回文件路径，**不**带 snippet（"too confusing"）
- 编辑工具内嵌 linter，语法错就拒绝写入
- 空输出显式说"ran successfully, no output"
- **守护栏只在 100% precision 时才加**（不能误杀）

但被 2026 的"filesystem-as-context"部分反转：当工具是通用 bash/read 时，让 agent 自己分页、grep、自己处理输出反而更好。

---

## C. 反模式（看着好但不付费）

| # | 反模式 | 来源 / 证据 |
|---|---|---|
| 1 | Parallel-writer swarm | Cognition Flappy Bird 案例：风格冲突、命名冲突、隐式架构假设撞车 |
| 2 | 过度 hierarchical 多层 agent 树 | Cognition："unstructured agent networks ... mostly a distraction" |
| 3 | 判官只看 diff 不跑测试 | PEV 模式要求 Verify 是 deterministic |
| 4 | 守护栏 false-positive 多 | SWE-agent："Don't add a guardrail unless you can show its FP rate is low" |
| 5 | 设计为 mid-task 改需求 | Devin 18 个月经验：最大失败模式 |
| 6 | 过早抽象成"通用 agent 框架" | Anthropic："simple, composable patterns" 胜出 |
| 7 | 手写 context compaction 启发式 | OpenAI Codex 2026：模型升级会内化它 |
| 8 | 类层级建模不同 agent | March 2026 论文：diamond problem |
| 9 | 100+ 特化工具替代通用 bash/file | Microsoft Azure SRE：45% → 75% 反向 |
| 10 | Runtime mode-switch 限权 | March 2026 论文："agents sometimes failed to exit plan mode" |

---

## D. 关键失败模式（2026 命名）

| 失败模式 | 含义 | 频次 |
|---|---|---|
| **Compounding Error Cascade** | 早期错误累积放大，到末期不可救 | 高 |
| **Context Drift** | 长会话中 agent 偏离原始目标 | 65% 企业 AI 失败的主因之一 |
| **Schema Misalignment** | 工具输入输出契约和 agent 期望不一致 | 65% 企业 AI 失败的主因之一 |
| **State Degradation** | 状态在多步操作中逐渐破损 | 65% 企业 AI 失败的主因之一 |
| **Implicit Decision Conflict** | 并行 agent 各自隐式假设撞车 | Cognition Flappy Bird 案例 |

---

## E. 给 hero-coding 的具体建议

约束：单机、story-in/commits-out、pi CLI 做执行器、OpenAI 兼容 API、TypeScript、不要花架子。

### 目标架构

```
inbox/us-XXX.md
       │
       ▼
   Dispatcher
   (worktree + state + 并发)
       │
       ▼
   ┌─── Round Loop ────────────────────────────────┐
   │                                               │
   │   ┌──────────────────────────────────┐        │
   │   │  Agent (单一抽象 = pi runner)     │        │
   │   └──────────────────────────────────┘        │
   │      ▲           ▲              ▲             │
   │      │           │              │             │
   │   [Worker]   [Verifier]      [Judge]          │
   │   Role A     Role B           Role C          │
   │   (写代码)   (跑测试)          (读 diff+测试结果)│
   │      │           │              │             │
   │      ▼           ▼              ▼             │
   │   git commit  test output   PASS/FAIL         │
   │                                               │
   └───────────────────────────────────────────────┘
                     │
              ┌──────┴──────┐
            PASS           FAIL
              │             │
              ▼             ▼
          done/        feedback append → next round
```

### 具体决策

| # | 决策 | 理由 |
|---|---|---|
| 1 | **Worker / Judge 抽象成 Agent + Role** | 验证：March 2026 论文 diamond problem |
| 2 | **加 Verifier 阶段，独立于 Judge** | PEV 模式；deterministic > probabilistic |
| 3 | **Judge 仍是 agent，prompt 包含 verifier 输出** | 双层证据：测试结果 + LLM 判读 |
| 4 | **不做 Planner** | story 已是 plan；YAGNI |
| 5 | **Reviewer pattern 暂不上，留接口** | 单 agent 仍是 30% 主流；等漏 bug 模式后再加 |
| 6 | **Reactive ReAct loop > 显式状态机** | pi 已经是；dispatcher 别 reinvent |
| 7 | **Event log 暂不上，但 trace 是种子** | 现有 state.json + traces 够 |
| 8 | **filesystem-standard ACI**（不要特化） | Azure SRE 数据 45% → 75% |
| 9 | **单线程主循环，inter-story 并发** | Cognition single-threaded writes |
| 10 | **Worker / Judge 用不同模型** | Cognition smart friend routing |
| 11 | **Schema-level 工具限制 Role 权限** | Judge Role 不应有 git commit；物理不给 |
| 12 | **Story frontmatter 加 `verify:` 和 `scope:`** | verify 喂 verifier；scope 给 auto-rescue 白名单 |

### 不该做的（明确说"不"）

- 不做 Planner agent
- 不做 Reviewer agent（暂时）
- 不做 multi-agent 并行写代码
- 不做 mid-task 需求改动支持
- 不抽象成"通用 agent 框架"——只服务 coding 这一个垂直
- 不做向量检索 / wiki —— pi 的 grep+read 在"清晰 story"场景已经够
- 不做 Docker sandbox —— git worktree 已经够隔离
- 不写复杂 context compaction —— 模型会内化它（OpenAI Codex 2026 教训）

### 实施优先级

1. **Verifier 阶段**（最大收益，1 天工作量）—— 把"judge 看 diff 猜"改成"judge 看真实测试结果做判决"
2. **Agent + Role 重构**（架构债务，1-2 天）—— 把 Worker/Judge 抽成 Role，加 verifier 顺手做
3. **Schema-level tool restriction**（半天）—— Role 数据加 `allowedTools: string[]`
4. **Story scope 字段 + auto-rescue 白名单**（半天）
5. **Cost / token 累计**（半天）
6. **Reviewer Role**（等跑过 20+ story、看到 judge 漏 bug 模式后再做）

---

## F. SWE-bench Verified 进度参考（2026-05）

- 早 2025：~65%
- 2025 末：~76%（Claude Opus 4.5）
- 2026-04：87.6%（Claude Opus 4.7 with Claude Code harness）
- 2026-04：88.7%（GPT-5.5）
- 2026-05：93.9%（Claude Mythos Preview，受限发布）

模型架构对分数贡献：5-15 个百分点（Scale 报告）。

---

## G. 阅读优先级清单（全量按 ROI 排序）

针对 hero-coding 当前阶段（全自动 coding harness、单机、pi 执行器、要做 Verifier + Agent+Role 重构）的 ROI 评估。
P0 是直接影响下一步设计的载重材料，P3 是可以跳过的。

### P0 — 必读（5 篇，约 2 小时）

| # | 标题 | 时长 | 为什么 P0 |
|---|---|---|---|
| 1 | [Anthropic — Building Effective Agents (2024-12)](https://www.anthropic.com/research/building-effective-agents) | 30min | 7 模式 + simplicity 哲学；所有架构决策的 first-principle 起点。**先读这个** |
| 2 | [OpenAI — Harness engineering blog (2026-02)](https://openai.com/index/harness-engineering/) | 15min | "Scaffolding is coping not scaling" + Bitter Lesson 框架。读完会让你删代码而不是加代码 |
| 3 | [arxiv 2603.05344 — Building AI Coding Agents for the Terminal (2026-03)](https://arxiv.org/abs/2603.05344) | 30min | 单参数化 MainAgent + schema-level 限权 + defense-in-depth 5 层。**直接对应你的 Agent+Role 重构** |
| 4 | [Cognition — Don't Build Multi-Agents (2025-09)](https://cognition.ai/blog/dont-build-multi-agents) | 15min | 单线程写代码原则；Flappy Bird 反例。决定你做不做 Reviewer/Critic |
| 5 | [Augment — Harness Engineering Constraints (2026)](https://www.augmentcode.com/guides/harness-engineering-ai-coding-agents) | 20min | PEV (Plan-Execute-Verify) + deterministic enforcement。**正是你下一步要做的 Verifier** |

### P1 — 强烈推荐（6 篇，约 2.5 小时）

| # | 标题 | 时长 | 为什么 P1 |
|---|---|---|---|
| 6 | [Cognition — Multi-Agents: What's Actually Working (2025末)](https://cognition.ai/blog/multi-agents-working) | 15min | Cognition 立场演化：reviewer 抓 ~2 bugs/PR (58% severe)。Reviewer Role 何时上的判据 |
| 7 | [Mitchell Hashimoto / Martin Fowler — Harness engineering for coding agent users (2026-02)](https://martinfowler.com/articles/harness-engineering.html) | 15min | "Engineering the harness" 词源 + 实战观察 |
| 8 | [arxiv 2604.18071 — Architectural Design Decisions in AI Agent Harnesses (2026-04)](https://arxiv.org/html/2604.18071v1) | 30min | 70 项目横向调查，5 大设计维度。看你在哪个聚类 |
| 9 | [Cognition — Devin 2025 Performance Review (2025末)](https://cognition.ai/blog/devin-annual-performance-review-2025) | 20min | 18 个月生产经验。哪种任务能 fully autonomous，哪种不行 |
| 10 | [12 Agentic Harness Patterns from Claude Code](https://generativeprogrammer.com/p/12-agentic-harness-patterns-from) | 15min | 速查清单，密度极高 |
| 11 | [Anthropic 2026 Agentic Coding Trends Report (PDF)](https://resources.anthropic.com/hubfs/2026%20Agentic%20Coding%20Trends%20Report.pdf) | 30min | 一手 2026 数据 |

### P2 — 选读（对症下药，按未来需求触发）

#### 当你要设计 ACI / 工具系统
- [SWE-agent ACI Documentation (2024)](https://swe-agent.com/0.7/background/aci/) — ACI 设计 do/don't 经典原则（2026 部分被 filesystem-as-context 反转，但底线原则仍有效）

#### 当你要做 Subagent / Reviewer
- [arxiv 2604.14228 — Dive into Claude Code (2026-04)](https://arxiv.org/html/2604.14228v1) — **只读 §3（reasoning/enforcement 分离）+ §10（vs OpenClaw 对比）+ §12（开放方向）**，其余跳过

#### 当你要升级状态层（event sourcing）
- [arxiv 2511.03690 — OpenHands V1 SDK (2025-11)](https://arxiv.org/html/2511.03690v1) — event-sourced state、opt-in sandboxing、agent/tool/workspace 分离

#### 当你要加可观测性 / 自动调优 harness
- [arxiv 2604.25850 — Agentic Harness Engineering: Observability-Driven Auto-Evolution (2026-04)](https://arxiv.org/abs/2604.25850) — 三大可观测性支柱

#### 当你想看自动化 harness 的长期方向
- [arxiv 2604.21003 — The Last Harness You'll Ever Build (2026-04)](https://arxiv.org/abs/2604.21003) — Meta-Evolution Loop

#### 当你的 story 开始变长（>30 min agent 工作）
- [OpenAI — Run long horizon tasks with Codex (2026-05)](https://developers.openai.com/blog/run-long-horizon-tasks-with-codex)

#### 工程实践对照
- [Red Hat — Harness engineering: Structured workflows (2026-04)](https://developers.redhat.com/articles/2026/04/07/harness-engineering-structured-workflows-ai-assisted-development)
- [LangChain — The Anatomy of an Agent Harness](https://www.langchain.com/blog/the-anatomy-of-an-agent-harness)

#### OpenAI Codex 实现细节
- [OpenAI — Unlocking the Codex harness: how we built the App Server](https://openai.com/index/unlocking-the-codex-harness/)
- [OpenAI — Unrolling the Codex agent loop](https://openai.com/index/unrolling-the-codex-agent-loop/)

#### 排行榜横向对比
- [arxiv 2506.17208 — Dissecting SWE-Bench Leaderboards (2025-06)](https://arxiv.org/abs/2506.17208) — 80 上榜方案对比

### P3 — 参考 / 可跳过

不是不好，是对 hero-coding 当下场景 ROI 低。

#### 学术研究方向（与你工程目标不重合）
- [arxiv 2603.03329 — AutoHarness](https://arxiv.org/abs/2603.03329) — 让模型自动合成 harness（研究方向）
- [arxiv 2603.25723 — Natural-Language Agent Harnesses](https://arxiv.org/abs/2603.25723) — 用自然语言写 harness 行为（研究方向）
- [arxiv 2603.28052 — Meta-Harness](https://arxiv.org/abs/2603.28052) — 端到端优化 harness（研究方向）
- [arxiv 2604.20801 — Multi-Agent Harnesses for Vulnerability Discovery](https://arxiv.org/abs/2604.20801) — 安全垂直
- [arxiv 2602.01655 — ProjDevBench](https://arxiv.org/html/2602.01655v1) — benchmark
- [SWE-agent paper (arxiv 2405.15793)](https://arxiv.org/abs/2405.15793) — 读 ACI Doc 即可，不必读论文
- [preprints.org — Harness Engineering for Language](https://www.preprints.org/frontend/manuscript/567757f184a1af99de64c01b54a2d366/download_pub) — 非同行评审
- [Yoonho Lee — Meta-Harness paper PDF](https://yoonholee.com/meta-harness/paper.pdf)

#### 二手转述 / 内容重复
- [InfoQ — OpenAI Introduces Harness Engineering (2026-02)](https://www.infoq.com/news/2026/02/openai-harness-engineering-codex/) — OpenAI 博客的转述
- [Cobus Greyling — The Rise of AI Harness Engineering](https://cobusgreyling.medium.com/the-rise-of-ai-harness-engineering-5f5220de393e) — 轻量评论
- [Adnan Masood — Agent Harness Engineering Control Plane](https://medium.com/@adnanmasood/agent-harness-engineering-the-rise-of-the-ai-control-plane-938ead884b1d) — 轻量评论

#### 资源索引（Bookmark 即可）
- [GitHub — awesome-harness-engineering](https://github.com/ai-boost/awesome-harness-engineering) — 资源汇总，按需查
- [SWE-bench Verified Leaderboard](https://www.swebench.com/verified.html) — 排行榜，按需看
- [Scale SWE-Bench Pro Leaderboard](https://labs.scale.com/leaderboard/swe_bench_pro_public)
- [SWE-Bench Pro 2026 — Why 46% beats 81%](https://www.morphllm.com/swe-bench-pro)

### 时间预算建议

| 你有多少时间 | 怎么读 |
|---|---|
| **2 小时** | 只读 P0 中的 #1, #2, #3 三篇 — 最关键三块拼图（first principles + Bitter Lesson + 直接对应方案） |
| **半天 (4-5 小时)** | 全部 P0（5 篇） + #5 (Augment) + #10 (12 patterns) — 完整知道下一步要做什么、怎么做、避免哪些坑 |
| **一天 (8 小时)** | P0 + P1 全部，建立完整 mental model |
| **触发式 (碎片时间)** | P2 按你当前在做的子问题查阅，P3 直接跳 |

**强烈建议的 2 小时入门组合**（互补且不重叠）：
1. Anthropic Building Effective Agents（first principles）
2. OpenAI Harness blog（什么不该做）
3. arxiv 2603.05344 Terminal Agent（你的方案该长什么样）

---

## H. 2025 vs 2026 立场变化总览

| 维度 | 2024-2025 主流 | 2026 立场 |
|---|---|---|
| ACI 设计 | 为 agent 设计特化工具 | filesystem + 标准工具更好 |
| Context 管理 | 手写 compaction 启发式 | 让模型自己处理（Codex 教训） |
| Multi-agent | 风行（AutoGPT、ChatDev） | 多 agent 加智能 OK，多 agent 写代码不行 |
| Planner | 显式独立 planner agent | subagent + read-only 工具 schema |
| 安全模型 | 单层 prompt 提示 | Defense-in-depth 5 层 |
| Agent 建模 | 类层级 / 多类不同 agent | 单参数化 MainAgent + Role 配置 |
| Verify | LLM-on-diff | PEV：deterministic Verify + LLM Judge |
| 守护栏 | 越多越好 | 100% precision 才加 |
| 复杂度 | 把所有问题塞进 harness | "Scaffolding is coping not scaling" |

---

## 结论

> 当前 hero-coding 架构的核心问题不是 Worker/Judge 抽象，而是 **Judge 缺乏确定性证据**。
>
> 下一步：**先加 Verifier 阶段**（跑测试），**再做 Agent+Role 重构**（顺手把 Verifier 也包成一个 Role），其余先不动。
>
> 不要朝 multi-agent / planner / reviewer 方向卷复杂度。简单架构 + 通用 ACI + 有真实证据的 judge，是 SOTA 系统的共同特征。

---

## I. 2026 Harness 论文清单（专题）

### I.1 OpenAI Harness 三部曲（博客，非 paper）

OpenAI 自己没发 arxiv paper，是一系列官方博客。

| # | 标题 | 时间 | 核心 |
|---|---|---|---|
| 1 | [Harness engineering: leveraging Codex in an agent-first world](https://openai.com/index/harness-engineering/) | 2026-02-13 | 主文。5 个月内部实验：100 万行代码无人工，1/10 时间 |
| 2 | [Unlocking the Codex harness: how we built the App Server](https://openai.com/index/unlocking-the-codex-harness/) | 2026 | App Server 实现细节 |
| 3 | [Unrolling the Codex agent loop](https://openai.com/index/unrolling-the-codex-agent-loop/) | 2026 | agent loop 内部展开 |

**词源公案**："Engineering the harness" 这个词其实是 [Mitchell Hashimoto (HashiCorp 创始人) 2026-02 先提的](https://martinfowler.com/articles/harness-engineering.html)，OpenAI 和 Anthropic 几周后跟进发了官方版本。所以严格说 OpenAI 不是首创者。

### I.2 2026 Harness arxiv 论文（按发表月）

#### 2026-02

| 编号 | 标题 | 一句话 |
|---|---|---|
| [2602.01655](https://arxiv.org/html/2602.01655v1) | ProjDevBench | benchmark：端到端项目开发评估 |

#### 2026-03

| 编号 | 标题 | 一句话 |
|---|---|---|
| [2603.03329](https://arxiv.org/abs/2603.03329) | **AutoHarness** | Gemini 2.5-Flash 自动合成 harness（让模型造 harness 给模型用） |
| [2603.05344](https://arxiv.org/abs/2603.05344) | ⭐ **Building Effective AI Coding Agents for the Terminal** | 单参数化 MainAgent、schema 限权、defense-in-depth 5 层 |
| [2603.25723](https://arxiv.org/abs/2603.25723) | **Natural-Language Agent Harnesses (NLAH)** | 用自然语言写 harness 行为，IHR 共享 runtime 执行 |
| [2603.28052](https://arxiv.org/abs/2603.28052) | **Meta-Harness** | end-to-end 优化模型 harnesses（不只 prompt，连工具/上下文一起优化） |

#### 2026-04

| 编号 | 标题 | 一句话 |
|---|---|---|
| [2604.14228](https://arxiv.org/html/2604.14228v1) | ⭐ **Dive into Claude Code** | 系统性拆解 Claude Code 设计空间，VILA-Lab 出品 |
| [2604.18071](https://arxiv.org/html/2604.18071v1) | ⭐ **Architectural Design Decisions in AI Agent Harnesses** | 70 项目调查，5 大设计维度，找出聚类模式 |
| [2604.20801](https://arxiv.org/abs/2604.20801) | Synthesizing Multi-Agent Harnesses for Vulnerability Discovery | 安全垂直，自动合成多 agent harness 找漏洞 |
| [2604.21003](https://arxiv.org/abs/2604.21003) | **The Last Harness You'll Ever Build** | Meta-Evolution Loop：从手工 harness engineering → 自动化 harness engineering |
| [2604.25850](https://arxiv.org/abs/2604.25850) | **Agentic Harness Engineering (AHE)** | 三大可观测性支柱：component / experience / decision observability，自动演化 harness |

#### 2026-05

| 编号 | 标题 | 一句话 |
|---|---|---|
| (调研中) | — | 这个月还在收材料阶段 |

### I.3 非 arxiv 但学术性的

| 来源 | 标题 |
|---|---|
| preprints.org | [Harness Engineering for Language (model-driven systems)](https://www.preprints.org/frontend/manuscript/567757f184a1af99de64c01b54a2d366/download_pub) — 非同行评审 |
| Yoonho Lee 个人页 | [Meta-Harness paper PDF](https://yoonholee.com/meta-harness/paper.pdf) |

### I.4 2026 行业一手报告（非 arxiv）

| 来源 | 标题 |
|---|---|
| Anthropic | [2026 Agentic Coding Trends Report (PDF)](https://resources.anthropic.com/hubfs/2026%20Agentic%20Coding%20Trends%20Report.pdf) |
| OpenAI Developers | [Run long horizon tasks with Codex (2026-05)](https://developers.openai.com/blog/run-long-horizon-tasks-with-codex) |
| Mitchell Hashimoto / Martin Fowler | ⭐ [Harness engineering for coding agent users](https://martinfowler.com/articles/harness-engineering.html) — 词源 |
| InfoQ | [OpenAI Introduces Harness Engineering (2026-02)](https://www.infoq.com/news/2026/02/openai-harness-engineering-codex/) |
| Cobus Greyling | [The Rise of AI Harness Engineering (2026-03)](https://cobusgreyling.medium.com/the-rise-of-ai-harness-engineering-5f5220de393e) |
| Red Hat Developer | [Harness engineering: Structured workflows (2026-04)](https://developers.redhat.com/articles/2026/04/07/harness-engineering-structured-workflows-ai-assisted-development) |
| Augment | [Harness Engineering for AI Coding Agents](https://www.augmentcode.com/guides/harness-engineering-ai-coding-agents) |
| LangChain | [The Anatomy of an Agent Harness](https://www.langchain.com/blog/the-anatomy-of-an-agent-harness) |
| Adnan Masood (Medium) | [Agent Harness Engineering — The Rise of the AI Control Plane (2026-04)](https://medium.com/@adnanmasood/agent-harness-engineering-the-rise-of-the-ai-control-plane-938ead884b1d) |
| GitHub | ⭐ [awesome-harness-engineering](https://github.com/ai-boost/awesome-harness-engineering) — 资源汇总 |

### I.5 推荐阅读顺序（5 篇精读）

1. **[OpenAI — Harness engineering blog (2026-02)](https://openai.com/index/harness-engineering/)** —— 业界主流叙事的发起点
2. **[Mitchell Hashimoto / Martin Fowler — Harness engineering for coding agent users](https://martinfowler.com/articles/harness-engineering.html)** —— 词源 + 实战观察
3. **[arxiv 2603.05344 — Building AI Coding Agents for the Terminal](https://arxiv.org/abs/2603.05344)** —— 最直接对应 hero-coding 场景
4. **[arxiv 2604.18071 — Architectural Design Decisions](https://arxiv.org/html/2604.18071v1)** —— 70 项目横向数据
5. **[arxiv 2604.21003 — The Last Harness You'll Ever Build](https://arxiv.org/abs/2604.21003)** —— 看长期方向（自动化 harness 演化）
