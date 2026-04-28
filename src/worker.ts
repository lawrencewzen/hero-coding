import { spawn } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";
import type { WorkerConfig } from "./config.js";
import type { WorkerRunStats } from "./stats.js";
import type { UserStory } from "./userStory.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

const WORKER_SYSTEM_PROMPT = `You are Worker — a focused, single-task coding agent.

You are given one user story. Read the acceptance criteria, work the codebase, finish.

Hard rules:
- Make atomic git commits — one logical change per commit. Use Conventional Commits.
- Each commit message must explain WHY in one short line, then list WHAT changed in bullets, then mention errors fixed since the previous commit.
- Run tests / type checks before declaring done. Fix and commit again if they fail.
- Do not modify files outside the explicit scope of the story.
- Stop and surface the blocker if a step fails three times in a row — do not loop.
- When acceptance criteria are met, write a final summary and stop.
`;

function buildPrompt(story: UserStory, captainFeedback?: string): string {
  const parts = [story.body];
  if (captainFeedback) {
    parts.push(
      "",
      "---",
      "Previous attempt was rejected by Judge. Address this in this round:",
      captainFeedback,
    );
  }
  return parts.join("\n");
}

export async function runWorker(opts: {
  story: UserStory;
  worker: WorkerConfig;
  targetRepo: string;
  round: number;
  captainFeedback?: string;
}): Promise<WorkerRunStats> {
  const { story, worker, targetRepo, round, captainFeedback } = opts;
  const prompt = buildPrompt(story, captainFeedback);

  const args = [
    "--mode",
    "json",
    "--no-session",
    "--provider",
    worker.provider,
    "--model",
    worker.model,
    "--system-prompt",
    WORKER_SYSTEM_PROMPT,
    "--print",
    prompt,
  ];

  const localBin = path.resolve(__dirname, "..", "node_modules", ".bin", "pi");
  const start = Date.now();
  const stats: WorkerRunStats = {
    round,
    wallMs: 0,
    toolUseByName: {},
    toolUseTotal: 0,
    tokensIn: 0,
    tokensOut: 0,
    exitCode: 0,
  };

  return new Promise<WorkerRunStats>((resolve, reject) => {
    const child = spawn(localBin, args, {
      cwd: targetRepo,
      env: process.env,
      stdio: ["ignore", "pipe", "pipe"],
    });

    let buf = "";
    child.stdout.on("data", (chunk: Buffer) => {
      buf += chunk.toString("utf-8");
      let idx;
      while ((idx = buf.indexOf("\n")) >= 0) {
        const line = buf.slice(0, idx).trim();
        buf = buf.slice(idx + 1);
        if (!line) continue;
        try {
          handleEvent(line, stats);
        } catch {
          /* tolerate non-JSON lines */
        }
      }
    });

    child.stderr.on("data", (chunk: Buffer) => {
      process.stderr.write(`[worker] ${chunk.toString("utf-8")}`);
    });

    child.on("error", reject);
    child.on("close", (code) => {
      stats.wallMs = Date.now() - start;
      stats.exitCode = code ?? 0;
      resolve(stats);
    });
  });
}

interface ToolCall {
  name?: string;
}
interface UsageBlock {
  input?: number;
  output?: number;
}
interface AssistantMessageEvent {
  type: string;
  toolCall?: ToolCall;
}
interface MessageUpdateEvent {
  type: "message_update";
  assistantMessageEvent: AssistantMessageEvent;
}
interface TurnEndEvent {
  type: "turn_end";
  message: { usage?: UsageBlock };
}

function handleEvent(line: string, stats: WorkerRunStats): void {
  const ev = JSON.parse(line) as { type?: string } & Record<string, unknown>;
  if (!ev.type) return;

  if (ev.type === "message_update") {
    const m = ev as unknown as MessageUpdateEvent;
    const inner = m.assistantMessageEvent;
    if (inner?.type === "toolcall_end") {
      const name = inner.toolCall?.name ?? "unknown";
      stats.toolUseByName[name] = (stats.toolUseByName[name] ?? 0) + 1;
      stats.toolUseTotal += 1;
    }
    return;
  }

  if (ev.type === "turn_end") {
    const t = ev as unknown as TurnEndEvent;
    const u = t.message?.usage;
    if (u) {
      stats.tokensIn += Number(u.input ?? 0);
      stats.tokensOut += Number(u.output ?? 0);
    }
  }
}
