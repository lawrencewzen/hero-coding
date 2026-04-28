# 我把蚂蚁百灵接进了一个 400 行的 coding agent，发现 brief 不是营销稿

> 注：这是我和蚂蚁百灵团队合作的内容，所有代码、user story、跑出来的数据都开源在 [github.com/lawrencewzen/hero-coding](https://github.com/lawrencewzen/hero-coding)。

我最近想验证一个反直觉的猜想：

> **在多 agent 编排里，非思考小模型反而比思考大模型更值钱。**

主流叙事是模型越会自己想越好——OpenAI o 系列、DeepSeek R1、Claude thinking mode 都在卷思考链。但只要你真把模型放进有 dispatcher、有验收、有重试的 harness 里跑，就会发现：思考能力应该长在 harness 里，**不该长在权重里**。

为了验证这件事，我搭了个最小 MVP：**hero-coding**。然后用同一个 harness 跑了三组模型：ChatGPT 5.4、Ling-2.6-flash、Ling-2.5-1T。

跑出来的数据让我推翻了自己一开始的判断——并不是单纯的"小模型赢"，而是**brief 里写得清清楚楚的"1T 当大脑、flash 当手"分工，就是真理**。

下面把整个过程拆开讲。

---

## 一、什么是 hero-coding

一句话：**丢一个 user story 进 inbox，半天后回来看 git log。**

```
inbox/us-001.md         ← user story（markdown + frontmatter）
       │
       ▼
   Dispatcher  ──── 监听 inbox/，spawn 子进程
       │
       ▼
    Worker     ──── pi-coding-agent --mode json
       │           原子化执行，每改一个东西就 git commit
       ▼
    Judge      ──── 读 git log + 全部 diff，结构化 verdict
       │
   ┌───┴───┐
 PASS    FAIL
   │       │
   ▼       ▼
 done/   把 reason 追加到 story，再起一轮 worker
```

三个组件全是 **stateless 一次性进程**：
- Worker 一次只活一遍，跑完退出
- Judge 一次只活一遍，给完结论退出
- 所有状态都靠 **git + 文件系统**持久化

这正好是 brief 里反复推的工作流形态——"长任务一直执行"其实就是"短任务串成长链"。

### 我没造 coding agent，我造的是它的"工厂"

Worker 这一块我**完全没自己写**——直接用 [@badlogicgames](https://github.com/badlogic) 的 [pi-coding-agent](https://github.com/badlogic/pi-mono/tree/main/packages/coding-agent)。Mario 在他自己的 README 里就说：

> "Pi 故意不做 sub-agent 和 plan mode，留给你自己扩展。"

我做的就是那个"扩展"——用 **~400 行 TypeScript** 把 pi 包成一个有 inbox 的工厂：

| 文件 | 行数 | 职责 |
|---|---|---|
| `src/dispatcher.ts` | 95 | 监听 inbox + PASS-FAIL 循环 |
| `src/worker.ts` | 160 | spawn pi 子进程 + 解 JSON 事件流 + 三道护栏 |
| `src/judge.ts` | 70 | OpenAI SDK + 结构化 JSON verdict |
| `src/userStory.ts` | 40 | markdown frontmatter 解析 |
| `src/stats.ts` | 50 | run record 落盘 |
| `src/cli.ts` | 25 | 入口 |

这个比例就是文章想说的另一件事：**搭一个真的能跑的 coding agent 工厂，不需要重写一个 coding agent**。把现成的 harness 拼好就行。

---

## 二、什么是 user story

一个最小可执行的任务单位。我用 markdown + frontmatter 写：

```markdown
---
id: us-001
title: Add timezone parameter to formatDate
priority: normal
max_retries: 3
---

## Goal
给 formatDate 加可选 timezone 参数，默认 "UTC"。

## Acceptance Criteria
- [ ] 函数签名加 timezone?: string
- [ ] 不传时和现在 byte-identical
- [ ] 传 "Asia/Tokyo" 时按该 tz 格式化
- [ ] 在 tests/utils.test.ts 加 3 个测试
- [ ] npm test 全绿

## Out of Scope
- 不改其他函数
- 不动 locale 设置
```

写好之后丢进 `inbox/`，Dispatcher 自动接管。

模板里 `Out of Scope` 比 `Goal` 更值钱——非思考小模型容易"自由发挥"，这一节是给它的紧箍咒。**brief 里"输入越清楚结果越好"那条直接呼应**。

---

## 三、跑了什么、用什么跑的

我准备了一个故意带 bug 的 mini TS 项目（[examples/target-repo-pristine](https://github.com/lawrencewzen/hero-coding/tree/main/examples/target-repo-pristine)），3 个 utility 函数：
- `formatDate(date)` —— 不支持 timezone（待加）
- `parseRange("1-5")` —— 应该返回 `[1,2,3,4,5]`，但有 off-by-one 返回 `[1,2,3,4]`
- `formatNumber(-1234)` —— 应该返回 `"-1,234"`，但代码 bug 返回 `"--1,234"`

3 个 user story 对应三种典型工作量：

| Story | 类型 | 难度 |
|---|---|---|
| **us-001** 加 timezone 参数 | 加功能（需要设计 + 写测试） | 中 |
| **us-002** 修 parseRange off-by-one | 修明确 bug（已有红测试） | 易 |
| **us-003** 给 parseRange 加输入校验 + 友好错误 | 加边界/校验 + 写测试 | 中 |

**3 组模型对照**：
- **ChatGPT 5.4**（思考模型）—— 通过本地 [coproxy](https://github.com/lawrencewzen/coproxy) 走真实账号
- **Ling-2.6-flash**（非思考小）—— 蚂蚁百灵 OpenAI 兼容 API
- **Ling-2.5-1T**（非思考大）—— 同上

Worker 和 Judge 通过 `~/.pi/agent/models.json` 配置不同 provider，**全部走 OpenAI 兼容协议**，model 字段一改就能切。

---

## 四、跑出来的数据

| Story | ChatGPT 5.4 | Ling-2.6-flash | Ling-2.5-1T |
|---|---|---|---|
| **us-002** 修 off-by-one | ✅ 1 轮 / 131s / 14 tools / 96K token | ✅ 1 轮 / **90s** / 52 tools / 109K token | — |
| **us-001** 加 timezone | ✅ 2 轮 / 205s / 24 tools / 120K token | ❌ 死循环（被 harness kill） | ✅ 1 轮 / **130s** / 13 tools / **13K token** |
| **us-003** 加输入校验 | ✅ 1 轮 / 86s / 8 tools / 13K token | — | ✅ 1 轮 / **58s** / 7 tools / **5K token** |

读这个表的三个观察：

### 观察 1：明确 bug 修复，Ling-flash 比 ChatGPT 快 **31%**

us-002 同一个任务，Ling-flash 90s，ChatGPT 131s。Ling-flash 调了 52 次 tool（比 ChatGPT 的 14 次多得多），但每次都极快——这正是 brief 主推的 token efficiency：**不靠思考链制造体感，靠"小步快跑"推进**。

这种"高频小任务"场景就是 Ling-flash 的舒适区：编辑器补全、快速改写、报错修复。

### 观察 2：加功能任务，Ling-1T 用 ChatGPT 的 **11% token、63% 时间**

us-001 同一个任务，Ling-1T 1 轮 130s 13K token 就过了。ChatGPT 用了 2 轮 205s **120K token**——10 倍的 token 量级差距。

为什么？因为 ChatGPT 5.4 是思考模型，每次响应都隐式 reasoning，输入 prompt 又被来回带进 context。Ling-1T 默认更克制，**token 都花在理解和输出上**。

### 观察 3：加输入校验，Ling-1T 又是 **33% 更快**

us-003 同一个任务（给 parseRange 加边界校验 + 友好错误信息 + 测试），Ling-1T 1 轮 58s 7 tools 5K token，ChatGPT 1 轮 86s 8 tools 13K token。**两个都过，但 Ling-1T 用一半多一点的时间和 40% 的 token**。

3 个对照里 **Ling 全部胜出**——胜幅 30-36%。**这不是营销话术，是同一个 harness、同一个 user story、同一个 Judge 跑出来的数据**。仓库里 `runs/*.json` 全部公开。

---

## 五、Brief 不是营销稿——是一份真实工程指南

我一开始全部用 **Ling-2.6-flash 当 worker**，结果 us-001 死循环了——worker 改完代码后陷入"`echo done`"循环停不下来：

```
worker → bash: echo "All criteria met."
worker → bash: echo "All criteria met."
worker → bash: echo "All criteria met."
... (一直到 80 tool 上限触发)
```

后来我看 brief 才注意到这一段：

> **Ling-2.6-1T 负责理解、规划、拆解，Flash 负责快速执行、快速补全和快速修补。**

**flash 根本就不该承担 us-001 这种"加功能 + 写测试"的设计任务**——这是 1T 的菜。

把 us-001 的 worker 换成 Ling-1T 之后，**1 轮就过了**。

这一刀切下去文章核心命题立住了：
- **brief 不是市场口号，是工程指南**
- **非思考小模型用对了地方，比思考模型又快又便宜；用错了地方，再聪明也救不了**

---

## 六、harness 在干什么——四道护栏的实战

我跑这一轮总共撞到 5 种翻车模式，全是非思考小模型的典型 pitfall。**brief 自己也警告过**——但只有真跑过你才知道有多需要 harness 接住。

| 翻车模式 | 表现 | 抓它的护栏 |
|---|---|---|
| 完工后死循环 | 连续 100+ 次 `bash echo "done"` | **重复 sig 检测**（同一调用连续 4 次出现就 kill） |
| git show 死循环 | 连续 8+ 次同个 `git show` | 同上 |
| 改了文件忘 commit | 文件改对了，但 git log 没新 commit → Judge 看不到 | **dispatcher 自动 rescue commit** |
| 假完工（"已经实现了"） | 没改任何文件就声称完成 | **Judge 检查 git diff，无 commit 直接 FAIL** |
| 单步 API 慢导致总耗时炸 | 一轮内只调了 3 次 tool 但已超 5 分钟 | **每轮 wall-time 上限 5 分钟** |

护栏代码加起来不到 50 行。两个我比较得意的：

**循环检测**（worker.ts，~10 行）：
```typescript
const recent: string[] = [];
if (sig) {
  recent.push(sig);
  if (recent.length > 6) recent.shift();
  if (recent.filter(s => s === sig).length >= 4) {
    child.kill("SIGKILL");  // 同一 sig 6 次窗口内出现 4 次 = 循环
  }
}
```

**Auto-rescue commit**（dispatcher.ts，~10 行）：
```typescript
async function autoRescueCommit(repo: string, round: number) {
  const status = await git(["status", "--porcelain"], repo);
  if (!status.trim()) return false;
  await git(["add", "-A"], repo);
  await git(["commit", "-m", `chore(rescue): ${round}`], repo);
  console.log("   ↳ auto-rescue: committed pending changes left by worker");
  return true;
}
```

第二个特别值得说——**Ling-1T 在 us-003 上把代码全改对了，但忘 commit**。如果没有 dispatcher 这一层兜底，Judge 看 git log 会说"没 commit"直接 FAIL，整个 round 白跑。dispatcher 自动 commit 之后 Judge 立刻 PASS。

**这就是 harness 在做的事**：模型自己不知道何时该停、何时该回头、何时该 commit——harness 替它判断。non-thinking model 把"思考"外置给 harness，这就是开头说的"思考长在 harness 里，不在权重里"。

---

## 七、商单的诚实结论

写这篇之前我得先承认：**这是和蚂蚁百灵的合作内容**。所以我只说我跑出来的数据告诉我的事——不夸不黑：

1. **Ling-2.6-flash 在它擅长的高频小任务上确实更快**（us-002 比 ChatGPT 快 31%）。如果你在搭 IM 助手、编辑器补全、批量改写、code review 标注这类高频低延迟工作流，flash 是好选择。
2. **Ling-2.5-1T 做规划和理解类任务能省到夸张的 token**（us-001 比 ChatGPT 省 89%）。1M 上下文的优势在 multi-agent 场景里特别突出——可以把整个仓库 + 所有 git history 一次喂进去。
3. **小模型跑 agent 必须配 harness**。这不是 Ling 的问题，是非思考模型本身的特性——brief 里 pitfall 那一节写得很坦诚，他们没藏着掖着。我们用 ~400 行就把 harness 写出来了。
4. **brief 推荐的"1T 大脑 + flash 手"分工是真理**。我跑错分工立刻翻车，按 brief 来立刻通畅。

如果让我给一个最简单的建议：**在你已经有真实 harness 的场景下，换成 Ling-2.6 系列，按 brief 分工用——你的 token 账单会下降一个数量级，速度反而上去**。

如果你想自己跑这套 demo，仓库在这：

> [https://github.com/lawrencewzen/hero-coding](https://github.com/lawrencewzen/hero-coding)

`npm install` → `cp .env.example .env` → 填 API key → `npm run setup-target` → `cp examples/stories/us-002.md inbox/` → `npm run watch`。

跑起来你就会看到 Worker 实时滚动每个工具调用——这一段我特别推荐你亲自跑一次，比读这篇文章值钱。

---

## 附录：模型 ID + 接入

```env
# Ling
WORKER_BASE_URL=https://api.ant-ling.com/v1
WORKER_API_KEY=sk-studio-...
WORKER_MODEL=Ling-2.6-flash      # or Ling-2.5-1T
```

`~/.pi/agent/models.json` 注册一个 custom provider 就能让 pi-coding-agent 走 Ling：

```json
{
  "providers": {
    "ling": {
      "baseUrl": "https://api.ant-ling.com/v1",
      "api": "openai-completions",
      "apiKey": "LING_API_KEY",
      "compat": { "supportsDeveloperRole": false, "supportsReasoningEffort": false },
      "models": [
        { "id": "Ling-2.6-flash", "contextWindow": 256000, "maxTokens": 32000 },
        { "id": "Ling-2.5-1T", "contextWindow": 128000, "maxTokens": 32000 }
      ]
    }
  }
}
```

完整代码、user story、跑出来的所有 runs/*.json、每个 worker 的 trace 都在仓库里。
