package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTree spins up a fake project root with the given file tree and
// returns the root path. Files are keyed by relative path.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const (
	ringYAML = `
name: ring
base_url: https://openrouter.ai/api/v1
default_model: inclusionai/ring-2.6-1t:free
default_reasoning_effort: high
`
	gpt5YAML = `
name: gpt-5
base_url: https://api.openai.com/v1
default_model: gpt-5
default_reasoning_effort: medium
`
	rolesRingOnly = `
worker:
  provider: ring
judge:
  provider: ring
target_repo: ./repo
`
	secretsBoth = `
keys:
  ring: sk-ring-test
  gpt-5: sk-gpt5-test
`
)

func TestLoad_HappyPath(t *testing.T) {
	root := writeTree(t, map[string]string{
		"config/providers/ring.yaml": ringYAML,
		"config/roles.yaml":          rolesRingOnly,
		"config.local.yaml":          secretsBoth,
		"repo/.keep":                 "",
	})

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	w, err := cfg.LLMFor("worker")
	if err != nil {
		t.Fatalf("LLMFor worker: %v", err)
	}
	if w.Model != "inclusionai/ring-2.6-1t:free" {
		t.Errorf("model: got %q", w.Model)
	}
	if w.ReasoningEffort != "high" {
		t.Errorf("effort: got %q", w.ReasoningEffort)
	}
	if w.APIKey != "sk-ring-test" {
		t.Errorf("api key: got %q", w.APIKey)
	}
	if w.BaseURL != "https://openrouter.ai/api/v1" {
		t.Errorf("base url: got %q", w.BaseURL)
	}
	if cfg.Target.Repo != filepath.Join(root, "repo") {
		t.Errorf("target repo: got %q", cfg.Target.Repo)
	}
	if cfg.Target.BaseRef != "main" {
		t.Errorf("base ref default: got %q", cfg.Target.BaseRef)
	}
}

func TestLLMFor_RoleOverridesProviderDefaults(t *testing.T) {
	root := writeTree(t, map[string]string{
		"config/providers/ring.yaml": ringYAML,
		"config/roles.yaml": `
worker:
  provider: ring
  model: some-other-model
  reasoning_effort: xhigh
judge:
  provider: ring
target_repo: /tmp
`,
		"config.local.yaml": secretsBoth,
	})

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	w, _ := cfg.LLMFor("worker")
	if w.Model != "some-other-model" {
		t.Errorf("worker model override: got %q", w.Model)
	}
	if w.ReasoningEffort != "xhigh" {
		t.Errorf("worker effort override: got %q", w.ReasoningEffort)
	}

	// judge inherits provider defaults
	j, _ := cfg.LLMFor("judge")
	if j.Model != "inclusionai/ring-2.6-1t:free" {
		t.Errorf("judge inherited model: got %q", j.Model)
	}
	if j.ReasoningEffort != "high" {
		t.Errorf("judge inherited effort: got %q", j.ReasoningEffort)
	}
}

func TestLLMFor_DifferentProviderPerRole(t *testing.T) {
	root := writeTree(t, map[string]string{
		"config/providers/ring.yaml":  ringYAML,
		"config/providers/gpt-5.yaml": gpt5YAML,
		"config/roles.yaml": `
worker:
  provider: ring
judge:
  provider: gpt-5
target_repo: /tmp
`,
		"config.local.yaml": secretsBoth,
	})

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	w, _ := cfg.LLMFor("worker")
	j, _ := cfg.LLMFor("judge")

	if w.BaseURL != "https://openrouter.ai/api/v1" {
		t.Errorf("worker base url: got %q", w.BaseURL)
	}
	if j.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("judge base url: got %q", j.BaseURL)
	}
	if j.ReasoningEffort != "medium" {
		t.Errorf("judge effort from gpt-5 default: got %q", j.ReasoningEffort)
	}
}

func TestLoad_MissingApiKey_ErrorsLoud(t *testing.T) {
	root := writeTree(t, map[string]string{
		"config/providers/ring.yaml": ringYAML,
		"config/roles.yaml":          rolesRingOnly,
		"config.local.yaml":          "keys: {}\n",
	})
	_, err := Load(root)
	if err == nil {
		t.Fatal("expected missing-key error")
	}
	if !strings.Contains(err.Error(), "missing api key") {
		t.Errorf("error wording: %v", err)
	}
}

func TestLoad_RoleReferencesUnknownProvider(t *testing.T) {
	root := writeTree(t, map[string]string{
		"config/providers/ring.yaml": ringYAML,
		"config/roles.yaml": `
worker:
  provider: ring
judge:
  provider: nonexistent
target_repo: /tmp
`,
		"config.local.yaml": secretsBoth,
	})
	_, err := Load(root)
	if err == nil {
		t.Fatal("expected unknown-provider error")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("error wording: %v", err)
	}
}

func TestLoad_NoProviders(t *testing.T) {
	root := writeTree(t, map[string]string{
		"config/providers/.keep": "",
		"config/roles.yaml":      rolesRingOnly,
		"config.local.yaml":      secretsBoth,
	})
	_, err := Load(root)
	if err == nil {
		t.Fatal("expected no-providers error")
	}
	if !strings.Contains(err.Error(), "no provider configs") {
		t.Errorf("error wording: %v", err)
	}
}

func TestLoad_MissingSecretsFile(t *testing.T) {
	root := writeTree(t, map[string]string{
		"config/providers/ring.yaml": ringYAML,
		"config/roles.yaml":          rolesRingOnly,
	})
	_, err := Load(root)
	if err == nil {
		t.Fatal("expected missing-secrets error")
	}
}

func TestLLMFor_ProviderWithoutDefaultModel_RoleMustOverride(t *testing.T) {
	root := writeTree(t, map[string]string{
		"config/providers/p.yaml": `
name: p
base_url: https://x.example/v1
`,
		"config/roles.yaml": `
worker:
  provider: p
judge:
  provider: p
  model: explicit-model
target_repo: /tmp
`,
		"config.local.yaml": "keys:\n  p: k\n",
	})
	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := cfg.LLMFor("worker"); err == nil {
		t.Error("expected error for worker (no model anywhere)")
	}
	j, err := cfg.LLMFor("judge")
	if err != nil {
		t.Fatalf("judge LLMFor: %v", err)
	}
	if j.Model != "explicit-model" {
		t.Errorf("judge model: got %q", j.Model)
	}
}

func TestLoad_RuntimeDefaults(t *testing.T) {
	root := writeTree(t, map[string]string{
		"config/providers/ring.yaml": ringYAML,
		"config/roles.yaml":          rolesRingOnly,
		"config.local.yaml":          secretsBoth,
	})
	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxRetries != 3 {
		t.Errorf("MaxRetries default: got %d", cfg.MaxRetries)
	}
	if cfg.MaxParallel != 2 {
		t.Errorf("MaxParallel default: got %d", cfg.MaxParallel)
	}
	if cfg.VerifyTimeout.Milliseconds() != 120_000 {
		t.Errorf("VerifyTimeout default: got %v", cfg.VerifyTimeout)
	}
}

func TestLoad_RuntimeOverrides(t *testing.T) {
	root := writeTree(t, map[string]string{
		"config/providers/ring.yaml": ringYAML,
		"config/roles.yaml": `
worker:
  provider: ring
judge:
  provider: ring
target_repo: /tmp
target_base_ref: develop
max_retries: 5
max_parallel: 8
verify_timeout_ms: 30000
default_verify:
  - go test ./...
  - go vet ./...
`,
		"config.local.yaml": secretsBoth,
	})
	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxRetries != 5 || cfg.MaxParallel != 8 {
		t.Errorf("retries/parallel: %d / %d", cfg.MaxRetries, cfg.MaxParallel)
	}
	if cfg.VerifyTimeout.Milliseconds() != 30_000 {
		t.Errorf("timeout: got %v", cfg.VerifyTimeout)
	}
	if cfg.Target.BaseRef != "develop" {
		t.Errorf("base ref: got %q", cfg.Target.BaseRef)
	}
	if len(cfg.DefaultVerifyCommands) != 2 {
		t.Errorf("default verify: got %v", cfg.DefaultVerifyCommands)
	}
}
