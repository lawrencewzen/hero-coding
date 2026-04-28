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

export const workerConfig: WorkerConfig = {
  provider: required("WORKER_PROVIDER"),
  model: required("WORKER_MODEL"),
};

export const judgeConfig: JudgeConfig = {
  baseUrl: required("JUDGE_BASE_URL"),
  apiKey: required("JUDGE_API_KEY"),
  model: required("JUDGE_MODEL"),
};

export const targetRepo = required("TARGET_REPO");
export const maxRetries = Number.parseInt(process.env.MAX_RETRIES ?? "3", 10);
