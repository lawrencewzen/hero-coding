import { spawn } from "node:child_process";
import { createWriteStream } from "node:fs";
import { promises as fs } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import type { WorkerConfig } from "./config.js";
import type { WorkerRunStats } from "./stats.js";
import { storyId, type UserStory } from "./userStory.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const TRACE_DIR = path.resolve(__dirname, "..", "runs", "traces");

const WORKER_SYSTEM_PROMPT = `You are Worker — a focused, single-task coding agent.

You are given one user story. Read the acceptance criteria, work the codebase, finish.

Hard rules:
- Make atomic git commits — one logical change per commit. Use Conventional Commits.
- Each commit message must explain WHY in one short line, then list WHAT changed in bullets, then mention errors fixed since the previous commit.
- Run tests / type checks before declaring done. Fix and commit again if they fail.
- Do not modify files outside the explicit scope of the story.
- Stop and surface the blocker if a step fails three times in a row — do not loop.

STOP CONDITION (read carefully):
When the acceptance criteria are met AND the working tree is clean AND tests pass:
  1. Write a final summary message in one assistant turn (text only, no tool calls).
  2. Then STOP. Do NOT call any more tools. Do NOT echo "done" or "all good" via bash.
  3. Calling another tool after success will be treated as a bug.
`;

const MAX_TOOL_CALLS = 80;
const MAX_WALL_MS = 5 * 60 * 1000; // 5 min per round
const LOOP_DETECT_WINDOW = 6; // last N tool calls
const LOOP_DETECT_THRESHOLD = 4; // identical sig appears >= K times in window

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

  await fs.mkdir(TRACE_DIR, { recursive: true });
  const traceFile = path.join(TRACE_DIR, `${storyId(story)}-r${round}-${Date.now()}.jsonl`);
  const trace = createWriteStream(traceFile, { encoding: "utf-8" });

  // Sliding window of recent tool-call signatures for loop detection.
  const recent: string[] = [];

  return new Promise<WorkerRunStats>((resolve, reject) => {
    const child = spawn(localBin, args, {
      cwd: targetRepo,
      env: process.env,
      stdio: ["ignore", "pipe", "pipe"],
    });

    const wallTimer = setTimeout(() => {
      if (!child.killed) {
        process.stderr.write(`     ⚠ wall-time cap (${MAX_WALL_MS}ms) — killing worker\n`);
        child.kill("SIGKILL");
        stats.killReason = "wall_timeout";
      }
    }, MAX_WALL_MS);

    let buf = "";
    child.stdout.on("data", (chunk: Buffer) => {
      buf += chunk.toString("utf-8");
      let idx;
      while ((idx = buf.indexOf("\n")) >= 0) {
        const line = buf.slice(0, idx).trim();
        buf = buf.slice(idx + 1);
        if (!line) continue;
        trace.write(`${line}\n`);
        try {
          const sig = handleEvent(line, stats);
          if (sig) {
            recent.push(sig);
            if (recent.length > LOOP_DETECT_WINDOW) recent.shift();
            // count occurrences of newest sig in window
            let n = 0;
            for (const s of recent) if (s === sig) n++;
            if (n >= LOOP_DETECT_THRESHOLD && !child.killed) {
              process.stderr.write(
                `     ⚠ loop detected (${n}× same call in last ${recent.length}) — killing worker\n`,
              );
              stats.killReason = "loop";
              child.kill("SIGKILL");
            }
          }
        } catch {
          /* tolerate non-JSON lines */
        }
        if (stats.toolUseTotal > MAX_TOOL_CALLS && !child.killed) {
          process.stderr.write(
            `     ⚠ tool-call cap (${MAX_TOOL_CALLS}) exceeded — killing worker\n`,
          );
          stats.killReason = "tool_cap";
          child.kill("SIGKILL");
        }
      }
    });

    child.stderr.on("data", (chunk: Buffer) => {
      process.stderr.write(`[pi-stderr] ${chunk.toString("utf-8")}`);
    });

    child.on("error", (err) => {
      clearTimeout(wallTimer);
      trace.end();
      reject(err);
    });
    child.on("close", (code) => {
      clearTimeout(wallTimer);
      trace.end();
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

function fmtToolArgs(tc: ToolCall | undefined): string {
  if (!tc) return "";
  const args = (tc as unknown as { arguments?: Record<string, unknown> }).arguments ?? {};
  if (tc.name === "bash") return String(args.cmd ?? args.command ?? "").slice(0, 80);
  if (tc.name === "read") return String(args.path ?? args.file ?? "");
  if (tc.name === "edit" || tc.name === "write")
    return String(args.path ?? args.file ?? "");
  return JSON.stringify(args).slice(0, 80);
}

function toolSig(tc: ToolCall | undefined): string {
  if (!tc) return "unknown";
  const args = (tc as unknown as { arguments?: Record<string, unknown> }).arguments ?? {};
  return `${tc.name}|${JSON.stringify(args)}`;
}

function handleEvent(line: string, stats: WorkerRunStats): string | null {
  const ev = JSON.parse(line) as { type?: string } & Record<string, unknown>;
  if (!ev.type) return null;

  if (ev.type === "message_update") {
    const m = ev as unknown as MessageUpdateEvent;
    const inner = m.assistantMessageEvent;
    if (inner?.type === "toolcall_end") {
      const name = inner.toolCall?.name ?? "unknown";
      stats.toolUseByName[name] = (stats.toolUseByName[name] ?? 0) + 1;
      stats.toolUseTotal += 1;
      const argSummary = fmtToolArgs(inner.toolCall);
      process.stderr.write(`     · ${name.padEnd(5)} ${argSummary}\n`);
      return toolSig(inner.toolCall);
    }
    if (inner?.type === "text_end") {
      const content = (inner as unknown as { content?: string }).content ?? "";
      const trimmed = content.trim();
      if (trimmed) {
        const oneline = trimmed.replace(/\s+/g, " ").slice(0, 110);
        process.stderr.write(`     # ${oneline}\n`);
      }
    }
    return null;
  }

  if (ev.type === "turn_end") {
    const t = ev as unknown as TurnEndEvent;
    const u = t.message?.usage;
    if (u) {
      stats.tokensIn += Number(u.input ?? 0);
      stats.tokensOut += Number(u.output ?? 0);
    }
  }
  return null;
}
