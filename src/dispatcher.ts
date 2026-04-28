import { execFile } from "node:child_process";
import { promises as fs } from "node:fs";
import path from "node:path";
import { promisify } from "node:util";
import { judgeConfig, maxRetries, targetRepo, workerConfig } from "./config.js";
import { runJudge } from "./judge.js";
import type { RunStats } from "./stats.js";
import { writeRun } from "./stats.js";
import { appendJudgeFeedback, parseStory, storyId, type UserStory } from "./userStory.js";
import { runWorker } from "./worker.js";

const exec = promisify(execFile);

const ROOT = path.resolve(path.dirname(new URL(import.meta.url).pathname), "..");
const INBOX = path.join(ROOT, "inbox");
const DONE = path.join(ROOT, "done");
const RUNS = path.join(ROOT, "runs");

async function git(args: string[], cwd: string): Promise<string> {
  const { stdout } = await exec("git", args, { cwd });
  return stdout.trim();
}

async function ensureBranch(repo: string, branch: string): Promise<string> {
  const headSha = await git(["rev-parse", "HEAD"], repo);
  await git(["checkout", "-B", branch], repo);
  return headSha;
}

async function countCommits(repo: string, base: string): Promise<number> {
  const out = await git(["rev-list", "--count", `${base}..HEAD`], repo);
  return Number.parseInt(out, 10);
}

export async function runOnce(storyPath: string): Promise<RunStats> {
  const story = await parseStory(storyPath);
  const sid = storyId(story);
  const branch = `hero/${sid}`;
  const startedAt = new Date().toISOString();

  console.log(`\n=== ${sid}: ${story.frontmatter.title} ===`);
  const baseSha = await ensureBranch(targetRepo, branch);

  const stats: RunStats = {
    storyId: sid,
    storyTitle: story.frontmatter.title,
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
  let outerStart = Date.now();

  for (let round = 1; round <= limit; round++) {
    console.log(`-- round ${round} / ${limit}`);
    const w = await runWorker({
      story,
      worker: workerConfig,
      targetRepo,
      round,
      captainFeedback: lastFeedback,
    });
    stats.workerRuns.push(w);
    console.log(
      `   worker: ${w.wallMs}ms, ${w.toolUseTotal} tool uses, exit ${w.exitCode}`,
    );

    const v = await runJudge({
      story,
      judge: judgeConfig,
      targetRepo,
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

  stats.commits = await countCommits(targetRepo, baseSha);
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
