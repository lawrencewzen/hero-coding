import { execFile } from "node:child_process";
import { promises as fs } from "node:fs";
import path from "node:path";
import { promisify } from "node:util";
import { judgeConfig, maxRetries, workerConfig, workspaceConfig } from "./config.js";
import { runJudge } from "./judge.js";
import type { RunStats } from "./stats.js";
import { writeRun } from "./stats.js";
import { appendJudgeFeedback, parseStory, storyId } from "./userStory.js";
import { runWorker } from "./worker.js";

const exec = promisify(execFile);

const ROOT = path.resolve(path.dirname(new URL(import.meta.url).pathname), "..");
const INBOX = path.join(ROOT, "inbox");
const DONE = path.join(ROOT, "done");
const RUNS = path.join(ROOT, "runs");
const WORKTREES = path.join(ROOT, "worktrees");

async function git(args: string[], cwd: string): Promise<string> {
  const { stdout } = await exec("git", args, { cwd });
  return stdout.trim();
}

async function removeBranchIfExists(repo: string, branch: string): Promise<void> {
  const branchRef = await git(["branch", "--list", branch], repo);
  if (!branchRef.trim()) return;
  await git(["branch", "-D", branch], repo);
}

async function prepareWorktree(opts: {
  repo: string;
  baseRef: string;
  branch: string;
  storyKey: string;
}): Promise<{ baseSha: string; worktreePath: string }> {
  const { repo, baseRef, branch, storyKey } = opts;
  const baseSha = await git(["rev-parse", baseRef], repo);
  const worktreePath = path.join(WORKTREES, storyKey);

  await fs.mkdir(WORKTREES, { recursive: true });
  try {
    await git(["worktree", "remove", "--force", worktreePath], repo);
  } catch {
    // no existing worktree registration for this path
  }
  await fs.rm(worktreePath, { recursive: true, force: true });
  await removeBranchIfExists(repo, branch);
  await git(["worktree", "add", "-b", branch, worktreePath, baseRef], repo);

  return { baseSha, worktreePath };
}

async function cleanupWorktree(repo: string, worktreePath: string): Promise<void> {
  try {
    await git(["worktree", "remove", "--force", worktreePath], repo);
  } catch {
    await fs.rm(worktreePath, { recursive: true, force: true });
  }
}

async function countCommits(repo: string, base: string): Promise<number> {
  const out = await git(["rev-list", "--count", `${base}..HEAD`], repo);
  return Number.parseInt(out, 10);
}

async function autoRescueCommit(repo: string, round: number): Promise<boolean> {
  // If worker left uncommitted changes, commit them so the Judge can see them.
  const status = await git(["status", "--porcelain"], repo);
  if (!status.trim()) return false;
  await git(["add", "-A"], repo);
  await git(
    ["commit", "-m", `chore(rescue): auto-commit pending worker changes (round ${round})`],
    repo,
  );
  console.log(`   ↳ auto-rescue: committed pending changes left by worker`);
  return true;
}

export async function runOnce(storyPath: string): Promise<RunStats> {
  const story = await parseStory(storyPath);
  const sid = storyId(story);
  const branch = `hero/${sid}`;
  const startedAt = new Date().toISOString();
  const storyKey = sid.replace(/[^A-Za-z0-9._-]/g, "_");

  console.log(`\n=== ${sid}: ${story.frontmatter.title} ===`);
  const { baseSha, worktreePath } = await prepareWorktree({
    repo: workspaceConfig.repo,
    baseRef: workspaceConfig.baseRef,
    branch,
    storyKey,
  });

  const stats: RunStats = {
    storyId: sid,
    storyTitle: story.frontmatter.title,
    branch,
    baseRef: workspaceConfig.baseRef,
    worktreePath,
    worker: { provider: workerConfig.provider, model: workerConfig.model },
    judge: { baseUrl: judgeConfig.baseUrl, model: judgeConfig.model },
    startedAt,
    workerRuns: [],
    verdicts: [],
    commits: 0,
    finalStatus: "running",
  };

  const limit = story.frontmatter.max_retries ?? maxRetries;
  let lastFeedback: string | undefined;
  const outerStart = Date.now();

  try {
    for (let round = 1; round <= limit; round++) {
      console.log(`-- round ${round} / ${limit}`);
      const w = await runWorker({
        story,
        worker: workerConfig,
        targetRepo: worktreePath,
        round,
        captainFeedback: lastFeedback,
      });
      stats.workerRuns.push(w);
      console.log(
        `   worker: ${w.wallMs}ms, ${w.toolUseTotal} tool uses, exit ${w.exitCode}`,
      );

      await autoRescueCommit(worktreePath, round);

      const v = await runJudge({
        story,
        judge: judgeConfig,
        targetRepo: worktreePath,
        baseRef: baseSha,
        round,
      });
      stats.verdicts.push(v);
      console.log(`   judge: ${v.verdict} — ${v.reason.slice(0, 120)}`);

      if (v.verdict === "PASS") {
        stats.finalStatus = "done";
        break;
      }
      lastFeedback = v.reason;
      await appendJudgeFeedback(storyPath, round, v.reason);
    }

    if (stats.finalStatus === "running") stats.finalStatus = "gave_up";

    stats.commits = await countCommits(worktreePath, baseSha);
    stats.finishedAt = new Date().toISOString();
    stats.totalWallMs = Date.now() - outerStart;

    const runFile = await writeRun(RUNS, stats);
    console.log(
      `=> ${stats.finalStatus.toUpperCase()} — ${stats.commits} commits, ${stats.totalWallMs}ms total. log: ${runFile}`,
    );

    if (stats.finalStatus === "done") {
      await fs.mkdir(DONE, { recursive: true });
      const dest = path.join(DONE, path.basename(storyPath));
      await fs.rename(storyPath, dest);
    }
  } finally {
    await cleanupWorktree(workspaceConfig.repo, worktreePath);
  }

  return stats;
}

export async function watch(): Promise<void> {
  await fs.mkdir(INBOX, { recursive: true });
  await fs.mkdir(DONE, { recursive: true });
  await fs.mkdir(RUNS, { recursive: true });

  // process anything already there, then watch for new
  const existing = (await fs.readdir(INBOX)).filter((f) => f.endsWith(".md"));
  for (const f of existing) {
    try {
      await runOnce(path.join(INBOX, f));
    } catch (e) {
      console.error(`failed processing ${f}:`, e);
    }
  }

  const chokidar = await import("chokidar");
  console.log(`watching ${INBOX} ...`);
  const watcher = chokidar.watch(INBOX, { ignoreInitial: true });
  watcher.on("add", async (file) => {
    if (!file.endsWith(".md")) return;
    try {
      await runOnce(file);
    } catch (e) {
      console.error("dispatch error:", e);
    }
  });
}
