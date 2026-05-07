package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config aggregates everything hero-coding needs at runtime, populated
// purely from environment variables (loaded by the caller from .env).
type Config struct {
	Worker WorkerConfig
	Judge  LLMConfig // re-uses the LLM client struct directly
	Target TargetConfig

	MaxRetries  int
	MaxParallel int

	DefaultVerifyCommands []string
	VerifyTimeout         time.Duration
}

// WorkerConfig is the worker LLM connection. Same shape as LLMConfig but
// kept as a distinct type so future worker-only knobs (tool budget,
// system prompt overrides, etc.) have a clear home.
type WorkerConfig struct {
	BaseURL string
	APIKey  string
	Model   string
}

func (w WorkerConfig) ToLLM() LLMConfig {
	return LLMConfig{BaseURL: w.BaseURL, APIKey: w.APIKey, Model: w.Model}
}

type TargetConfig struct {
	Repo    string
	BaseRef string
}

// Load reads the process environment and returns a validated Config.
// Required vars: WORKER_BASE_URL, WORKER_API_KEY, WORKER_MODEL,
// JUDGE_BASE_URL, JUDGE_API_KEY, JUDGE_MODEL, TARGET_REPO.
func Load() (*Config, error) {
	cfg := &Config{
		Worker: WorkerConfig{
			BaseURL: os.Getenv("WORKER_BASE_URL"),
			APIKey:  os.Getenv("WORKER_API_KEY"),
			Model:   os.Getenv("WORKER_MODEL"),
		},
		Judge: LLMConfig{
			BaseURL: os.Getenv("JUDGE_BASE_URL"),
			APIKey:  os.Getenv("JUDGE_API_KEY"),
			Model:   os.Getenv("JUDGE_MODEL"),
		},
		Target: TargetConfig{
			Repo:    os.Getenv("TARGET_REPO"),
			BaseRef: getEnvDefault("TARGET_BASE_REF", "main"),
		},
		MaxRetries:            getEnvInt("MAX_RETRIES", 3),
		MaxParallel:           max1(getEnvInt("MAX_PARALLEL", 2)),
		DefaultVerifyCommands: splitNonEmpty(os.Getenv("HERO_DEFAULT_VERIFY"), "\n"),
		VerifyTimeout:         time.Duration(max1(getEnvInt("HERO_VERIFY_TIMEOUT_MS", 120_000))) * time.Millisecond,
	}

	required := map[string]string{
		"WORKER_BASE_URL": cfg.Worker.BaseURL,
		"WORKER_API_KEY":  cfg.Worker.APIKey,
		"WORKER_MODEL":    cfg.Worker.Model,
		"JUDGE_BASE_URL":  cfg.Judge.BaseURL,
		"JUDGE_API_KEY":   cfg.Judge.APIKey,
		"JUDGE_MODEL":     cfg.Judge.Model,
		"TARGET_REPO":     cfg.Target.Repo,
	}
	for k, v := range required {
		if v == "" {
			return nil, fmt.Errorf("missing required env var: %s", k)
		}
	}
	return cfg, nil
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

func splitNonEmpty(s, sep string) []string {
	if s == "" {
		return nil
	}
	out := make([]string, 0)
	for _, p := range strings.Split(s, sep) {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
