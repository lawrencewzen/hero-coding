import { promises as fs } from "node:fs";
import path from "node:path";

export interface ToolUseRecord {
  name: string;
  count: number;
}

export interface WorkerRunStats {
  round: number;
  wallMs: number;
  toolUseByName: Record<string, number>;
  toolUseTotal: number;
  tokensIn: number;
  tokensOut: number;
  exitCode: number;
}

export interface VerdictRecord {
  round: number;
  verdict: "PASS" | "FAIL";
  reason: string;
  judgeWallMs: number;
}

export interface RunStats {
  storyId: string;
  storyTitle: string;
  worker: { baseUrl: string; model: string };
  judge: { baseUrl: string; model: string };
  startedAt: string;
  finishedAt?: string;
  totalWallMs?: number;
  workerRuns: WorkerRunStats[];
  verdicts: VerdictRecord[];
  commits: number;
  finalStatus: "done" | "gave_up" | "running";
}

export async function writeRun(runsDir: string, stats: RunStats): Promise<string> {
  await fs.mkdir(runsDir, { recursive: true });
  const ts = stats.startedAt.replace(/[:.]/g, "-");
  const file = path.join(runsDir, `${stats.storyId}-${ts}.json`);
  await fs.writeFile(file, JSON.stringify(stats, null, 2), "utf-8");
  return file;
}
