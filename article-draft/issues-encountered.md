# 实测过程中踩到的坑（按出现顺序）

文章里挑哪几个用、哪几个跳过，待定。所有问题都已在代码里修好。

| # | 问题 | 表现 | 根因 | 修法 | 文章价值 |
|---|---|---|---|---|---|
| 1 | gray-matter 把 `created: 2026-04-28` 解成 JS Date | Zod schema 报 `expected string, received date` | YAML 1.1 自动把日期字面量转 Date 对象 | Zod 用 `z.union([z.string(), z.date()]).transform(...)` | 低，工程细节 |
| 2 | target-repo 作为 nested git repo 被外层 git 当成 submodule | `git add` 提示 "embedded git repository" | examples/target-repo 自己也有 .git 目录 | gitignore `examples/target-repo/.git/`，加 setup-target.sh 在跑前重新 init | 中，"工程隔离" 主题可用 |
| 3 | coproxy 走 HTTPS 自签证书 | OpenAI SDK / pi 都拒绝连接 | 本地 reverse proxy 用 `localhost:6890` HTTPS + 自签 cert | `NODE_TLS_REJECT_UNAUTHORIZED=0`（仅 dev）+ models.json baseUrl 改 https | 低，本地开发场景特殊 |
| 4 | **Ling-2.6-flash 跑完任务后陷入"echo done"死循环** | trace 末尾连续 100+ 次 `bash echo "All criteria met."` | 非思考小模型不知道何时该 STOP，被 toolResult 成功反馈推着继续 | 强化 STOP CONDITION prompt + 80 tool 上限 + 5 分钟 wall-time + 重复 sig 检测 | **★★★ 高**，brief 警告的活体证据 |
| 5 | Ling-2.6-flash 跑 us-001 卡 `git show` 循环 | 80 tool 上限触发 | 同 #4，非思考小模型在不熟的任务上不知道终止 | 同 #4 | ★★★ 高，同上 |
| 6 | **target-repo 状态在多次跑之间被污染** | gpt-5.4 跑过 us-001 之后修改了文件，setup-target 只重置 .git 不重置文件 → Ling 进来看见"已经实现了" | setup-target.sh 设计缺陷 | 加 `examples/target-repo-pristine/` 真源 + rsync --delete 真重置 | **★ 中**，方法论严谨性可一笔带过 |
| 7 | **Ling-1T 把代码改对了但漏了 `git commit`** | git status 显示 `M src/utils.ts` 但 git log 没新 commit → Judge FAIL "no commits or diffs" | 原 system prompt "make atomic git commits" 不够硬 | 把 GIT COMMIT RULE 提到 prompt 顶部 + "edit 完必须立刻 commit"硬约束 | **★★★ 高**，**Judge 价值最锋利的演示** |
| 8 | zsh `rm -f inbox/*.md` 在空目录报 `no matches found` | 命令失败 exit 1 | zsh 默认开启 nomatch 选项 | 加 `2>/dev/null` | 极低，跳过 |

## 模型行为观察（不是 bug，但值得记录）

| 观察 | 数据 | 文章价值 |
|---|---|---|
| **Ling-2.6-flash 比 gpt-5.4 跑明确 bug 修复任务快 ~30%** | us-002: 90s vs 131s，1 轮 PASS | ★★★ 高 |
| Ling-2.6-flash 在简单任务上调更多次工具但每次更快 | us-002: Ling 52 tools / gpt-5.4 14 tools，整体仍更快 | ★★ 中，反差有意思 |
| **Judge 真在干活，不是橡皮图章** | us-001 baseline 第一轮被 Judge 打回，因为 worker 把无 timezone 时的路径也改了；us-001 Ling-1T 第一版被 Judge 打回 "no commits" | ★★★ 高 |
| **gpt-5.4 思考模型在 vague-spec 任务上 10 分钟两轮都没过** | us-003: 总 592s，44 tool uses，265K input tokens，最终 GAVE_UP | ★★★ 高，brief 主推论的实证 |
| 非思考小模型按 brief 推荐的角色用，结果就好 | us-001 用 flash 翻车 → 改用 1T 立刻过 | ★★★ 高，brief 不是营销稿 |
