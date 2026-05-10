// Package config loads hero-coding's runtime configuration from a layered
// set of YAML files rooted at the project directory.
//
//	<root>/config/providers/*.yaml   one file per LLM provider (no secrets)
//	<root>/config/roles.yaml         which provider plays which role + runtime knobs
//	<root>/config.local.yaml         API keys (gitignored)
//
// Resolution rules for a role's effective LLM config:
//
//	model            = role.model            OR provider.default_model
//	reasoning_effort = role.reasoning_effort OR provider.default_reasoning_effort
//	api_key          = secrets.keys[<provider name>]
//
// An empty reasoning_effort means the field is omitted from chat requests
// (correct for models that do not support a thinking budget).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ProviderConfig is one resolved LLM endpoint, ready to attach an API key.
type ProviderConfig struct {
	Name                   string
	BaseURL                string
	APIKey                 string // injected from config.local.yaml
	InsecureTLS            bool
	DefaultModel           string
	DefaultReasoningEffort string
}

// RoleAssignment binds a role ("worker", "judge") to a provider, with
// optional per-role overrides for model and reasoning effort.
type RoleAssignment struct {
	Provider        string
	Model           string // "" → fall back to provider.DefaultModel
	ReasoningEffort string // "" → fall back to provider.DefaultReasoningEffort
}

// Config is the validated runtime configuration. Construct with Load();
// resolve per-role connections via LLMFor.
type Config struct {
	Providers map[string]ProviderConfig // keyed by provider name (verbatim from yaml)
	Roles     map[string]RoleAssignment // "worker" / "judge"

	Target TargetConfig

	MaxRetries  int
	MaxParallel int

	DefaultVerifyCommands []string
	VerifyTimeout         time.Duration
}

type TargetConfig struct {
	Repo    string
	BaseRef string
}

// ---------------------------------------------------------------------------
// YAML schemas (private — only used during Load)
// ---------------------------------------------------------------------------

type providerYAML struct {
	Name                   string `yaml:"name"`
	BaseURL                string `yaml:"base_url"`
	InsecureTLS            bool   `yaml:"insecure_tls,omitempty"`
	DefaultModel           string `yaml:"default_model"`
	DefaultReasoningEffort string `yaml:"default_reasoning_effort,omitempty"`
}

type roleYAML struct {
	Provider        string `yaml:"provider"`
	Model           string `yaml:"model,omitempty"`
	ReasoningEffort string `yaml:"reasoning_effort,omitempty"`
}

type rolesYAML struct {
	Worker          roleYAML `yaml:"worker"`
	Reviewer        roleYAML `yaml:"reviewer"`
	Planner         roleYAML `yaml:"planner"`
	TargetRepo      string   `yaml:"target_repo"`
	TargetBaseRef   string   `yaml:"target_base_ref,omitempty"`
	MaxRetries      *int     `yaml:"max_retries,omitempty"`
	MaxParallel     *int     `yaml:"max_parallel,omitempty"`
	DefaultVerify   []string `yaml:"default_verify,omitempty"`
	VerifyTimeoutMS *int     `yaml:"verify_timeout_ms,omitempty"`
}

type secretsYAML struct {
	Keys map[string]string `yaml:"keys"`
}

// ---------------------------------------------------------------------------
// Load
// ---------------------------------------------------------------------------

// Load reads config from <root>/config and <root>/config.local.yaml.
// Relative paths inside roles.yaml (e.g. target_repo) are resolved against root.
func Load(root string) (*Config, error) {
	providers, err := loadProviders(filepath.Join(root, "config", "providers"))
	if err != nil {
		return nil, err
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("no provider configs found under %s/config/providers/*.yaml", root)
	}

	rolesPath := filepath.Join(root, "config", "roles.yaml")
	roles, target, runtime, err := loadRoles(rolesPath)
	if err != nil {
		return nil, err
	}

	secretsPath := filepath.Join(root, "config.local.yaml")
	secrets, err := loadSecrets(secretsPath)
	if err != nil {
		return nil, err
	}

	// Inject API keys.
	for name, p := range providers {
		key, ok := secrets[name]
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("provider %q: missing api key (set keys.%s in %s)", name, name, secretsPath)
		}
		p.APIKey = key
		providers[name] = p
	}

	// Validate roles point to known providers.
	for role, ra := range roles {
		if _, ok := providers[ra.Provider]; !ok {
			return nil, fmt.Errorf("role %q in %s references unknown provider %q (known: %v)",
				role, rolesPath, ra.Provider, sortedKeys(providers))
		}
	}

	if strings.TrimSpace(target.Repo) == "" {
		return nil, fmt.Errorf("target_repo is required in %s", rolesPath)
	}
	if !filepath.IsAbs(target.Repo) {
		target.Repo = filepath.Join(root, target.Repo)
	}
	if target.BaseRef == "" {
		target.BaseRef = "main"
	}

	cfg := &Config{
		Providers:             providers,
		Roles:                 roles,
		Target:                target,
		MaxRetries:            orDefault(runtime.MaxRetries, 3),
		MaxParallel:           max1(orDefault(runtime.MaxParallel, 2)),
		DefaultVerifyCommands: runtime.DefaultVerify,
		VerifyTimeout:         time.Duration(max1(orDefault(runtime.VerifyTimeoutMS, 120_000))) * time.Millisecond,
	}
	return cfg, nil
}

// LLMFor resolves a role binding ("worker", "judge") into the connection
// LLMClient needs, applying the role > provider fallback chain.
func (c *Config) LLMFor(role string) (LLMConfig, error) {
	ra, ok := c.Roles[role]
	if !ok {
		return LLMConfig{}, fmt.Errorf("role %q not configured", role)
	}
	p, ok := c.Providers[ra.Provider]
	if !ok {
		// Unreachable if Load() succeeded, but defend against direct Config construction in tests.
		return LLMConfig{}, fmt.Errorf("role %q: unknown provider %q", role, ra.Provider)
	}

	model := ra.Model
	if model == "" {
		model = p.DefaultModel
	}
	if model == "" {
		return LLMConfig{}, fmt.Errorf("role %q: no model configured (neither role.model nor provider %q default_model is set)", role, ra.Provider)
	}

	effort := ra.ReasoningEffort
	if effort == "" {
		effort = p.DefaultReasoningEffort
	}

	return LLMConfig{
		BaseURL:         p.BaseURL,
		APIKey:          p.APIKey,
		Model:           model,
		InsecureTLS:     p.InsecureTLS,
		ReasoningEffort: effort,
	}, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func loadProviders(dir string) (map[string]ProviderConfig, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read providers dir %s: %w", dir, err)
	}
	out := map[string]ProviderConfig{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !(strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml")) {
			continue
		}
		path := filepath.Join(dir, n)
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		var p providerYAML
		if err := yaml.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if strings.TrimSpace(p.Name) == "" {
			return nil, fmt.Errorf("%s: name is required", path)
		}
		if strings.TrimSpace(p.BaseURL) == "" {
			return nil, fmt.Errorf("%s: base_url is required", path)
		}
		if _, dup := out[p.Name]; dup {
			return nil, fmt.Errorf("%s: provider %q already defined", path, p.Name)
		}
		out[p.Name] = ProviderConfig{
			Name:                   p.Name,
			BaseURL:                p.BaseURL,
			InsecureTLS:            p.InsecureTLS,
			DefaultModel:           p.DefaultModel,
			DefaultReasoningEffort: p.DefaultReasoningEffort,
		}
	}
	return out, nil
}

func loadRoles(path string) (map[string]RoleAssignment, TargetConfig, rolesYAML, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, TargetConfig{}, rolesYAML{}, fmt.Errorf("read %s: %w", path, err)
	}
	var r rolesYAML
	if err := yaml.Unmarshal(raw, &r); err != nil {
		return nil, TargetConfig{}, rolesYAML{}, fmt.Errorf("parse %s: %w", path, err)
	}
	roles := map[string]RoleAssignment{}
	for name, src := range map[string]roleYAML{"worker": r.Worker, "reviewer": r.Reviewer, "planner": r.Planner} {
		if strings.TrimSpace(src.Provider) == "" {
			return nil, TargetConfig{}, rolesYAML{}, fmt.Errorf("%s: %s.provider is required", path, name)
		}
		roles[name] = RoleAssignment{
			Provider:        src.Provider,
			Model:           src.Model,
			ReasoningEffort: src.ReasoningEffort,
		}
	}
	return roles, TargetConfig{Repo: r.TargetRepo, BaseRef: r.TargetBaseRef}, r, nil
}

func loadSecrets(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w (create it from config.local.yaml.example and fill in api keys)", path, err)
	}
	var s secretsYAML
	if err := yaml.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if s.Keys == nil {
		s.Keys = map[string]string{}
	}
	return s.Keys, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func orDefault(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
