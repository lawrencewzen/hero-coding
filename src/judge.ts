import { execFile } from "node:child_process";
import { promisify } from "node:util";
import OpenAI from "openai";
import { z } from "zod";
import type { JudgeConfig } from "./config.js";
import type { VerdictRecord } from "./stats.js";
import type { UserStory } from "./userStory.js";

const exec = promisify(execFile);

const JUDGE_SYSTEM = `You are Captain — a strict but fair code reviewer.

You read a user story and the resulting git history (commits + diffs). You give a structured verdict.

Decide:
- PASS if every Acceptance Criteria is clearly met by the diffs and commit history,
  AND the work respects Constraints / Out of Scope.
- FAIL otherwise. In FAIL, give one paragraph of concrete, actionable feedback the next worker round can address. Reference specific files / commits if useful.

Reply with ONLY a single JSON object, no prose, no code fences:
{"verdict": "PASS" | "FAIL", "reason": string}
`;

const VerdictSchema = z.object({
  verdict: z.enum(["PASS", "FAIL"]),
  reason: z.string(),
});

async function git(repo: string, args: string[]): Promise<string> {
  const { stdout } = await exec("git", args, { cwd: repo, maxBuffer: 64 * 1024 * 1024 });
  return stdout;
}

async function collectGitContext(targetRepo: string, baseRef: string): Promise<string> {
  const log = await git(targetRepo, [
    "log",
    "--reverse",
    "--pretty=format:### %h %s%n%n%b%n",
    `${baseRef}..HEAD`,
  ]);
  const diff = await git(targetRepo, ["diff", `${baseRef}..HEAD`, "--stat"]);
  const fullDiff = await git(targetRepo, ["diff", `${baseRef}..HEAD`]);
  return [
    "## Commits since base",
    log || "(no commits)",
    "",
    "## Diff stat",
    diff || "(empty)",
    "",
    "## Full diff",
    fullDiff || "(empty)",
  ].join("\n");
}

export async function runJudge(opts: {
  story: UserStory;
  judge: JudgeConfig;
  targetRepo: string;
  baseRef: string;
  round: number;
}): Promise<VerdictRecord> {
  const { story, judge, targetRepo, baseRef, round } = opts;
  const ctx = await collectGitContext(targetRepo, baseRef);
  const client = new OpenAI({ baseURL: judge.baseUrl, apiKey: judge.apiKey });

  const userMsg = [
    "# User Story",
    story.body,
    "",
    "# Git Context",
    ctx,
  ].join("\n");

  const start = Date.now();
  const resp = await client.chat.completions.create({
    model: judge.model,
    messages: [
      { role: "system", content: JUDGE_SYSTEM },
      { role: "user", content: userMsg },
    ],
    temperature: 0.1,
    response_format: { type: "json_object" },
  });
  const judgeWallMs = Date.now() - start;

  const text = resp.choices[0]?.message?.content ?? "{}";
  let parsed: { verdict: "PASS" | "FAIL"; reason: string };
  try {
    parsed = VerdictSchema.parse(JSON.parse(text));
  } catch {
    parsed = { verdict: "FAIL", reason: `Judge returned malformed output: ${text.slice(0, 500)}` };
  }

  return { round, verdict: parsed.verdict, reason: parsed.reason, judgeWallMs };
}
