import "dotenv/config";

function required(key: string): string {
  const v = process.env[key];
  if (!v) throw new Error(`Missing required env var: ${key}`);
  return v;
}

export interface WorkerConfig {
  provider: string; // matches a provider key in ~/.pi/agent/models.json
  model: string;
}

export interface JudgeConfig {
  baseUrl: string;
  apiKey: string;
  model: string;
}

export interface WorkspaceConfig {
  repo: string;
  baseRef: string;
}

export const workerConfig: WorkerConfig = {
  provider: required("WORKER_PROVIDER"),
  model: required("WORKER_MODEL"),
};

export const judgeConfig: JudgeConfig = {
  baseUrl: required("JUDGE_BASE_URL"),
  apiKey: required("JUDGE_API_KEY"),
  model: required("JUDGE_MODEL"),
};

export const workspaceConfig: WorkspaceConfig = {
  repo: required("TARGET_REPO"),
  baseRef: process.env.TARGET_BASE_REF || "main",
};

export const targetRepo = workspaceConfig.repo;
export const maxRetries = Number.parseInt(process.env.MAX_RETRIES ?? "3", 10);
