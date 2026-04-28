import { promises as fs } from "node:fs";
import path from "node:path";
import matter from "gray-matter";
import { z } from "zod";

const FrontmatterSchema = z.object({
  id: z.string(),
  title: z.string(),
  created: z.string().optional(),
  priority: z.enum(["low", "normal", "high"]).default("normal"),
  max_retries: z.number().int().nonnegative().optional(),
});

export type UserStoryFrontmatter = z.infer<typeof FrontmatterSchema>;

export interface UserStory {
  filepath: string;
  frontmatter: UserStoryFrontmatter;
  body: string;
  raw: string;
}

export async function parseStory(filepath: string): Promise<UserStory> {
  const raw = await fs.readFile(filepath, "utf-8");
  const parsed = matter(raw);
  const fm = FrontmatterSchema.parse(parsed.data);
  return { filepath, frontmatter: fm, body: parsed.content.trim(), raw };
}

export async function appendJudgeFeedback(
  filepath: string,
  round: number,
  reason: string,
): Promise<void> {
  const raw = await fs.readFile(filepath, "utf-8");
  const sep = "\n\n## Captain Feedback (auto)\n";
  const block = `\n### Round ${round} — FAIL\n${reason}\n`;
  const next = raw.includes(sep) ? raw + block : raw + sep + block;
  await fs.writeFile(filepath, next, "utf-8");
}

export function storyId(story: UserStory): string {
  return story.frontmatter.id || path.basename(story.filepath, ".md");
}
