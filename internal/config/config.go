package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"hero-coding/internal/provider"
)

// Config is the validated runtime configuration. Construct with Load();
// resolve per-role connections via LLMFor.
type Config struct {
	Providers provider.Registry
	Roles     map[string]provider.Binding // "worker" -> {Provider, Model}

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

// Load reads providers + role bindings + workspace settings from env.
//
// Required:
//   - At least one HERO_PROVIDER_<name>_{BASE_URL,API_KEY} pair
//   - HERO_WORKER and HERO_JUDGE bindings, each "<provider>/<model>"
//   - TARGET_REPO
func Load() (*Config, error) {
	reg, err := provider.LoadRegistry()
	if err != nil {
		return nil, err
	}
	if len(reg) == 0 {
		return nil, fmt.Errorf("no providers defined; set at least one HERO_PROVIDER_<name>_BASE_URL + HERO_PROVIDER_<name>_API_KEY")
	}

	roles := map[string]provider.Binding{}
	for _, role := range []string{"worker", "judge"} {
		envKey := "HERO_" + strings.ToUpper(role)
		raw := os.Getenv(envKey)
		if raw == "" {
			return nil, fmt.Errorf("missing role binding %s (expected %q, e.g. coproxy/gpt-5.4)", envKey, "<provider>/<model>")
		}
		b, err := provider.ParseBinding(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", envKey, err)
		}
		if _, err := reg.Get(b.Provider, envKey); err != nil {
			return nil, err
		}
		roles[role] = b
	}

	repo := os.Getenv("TARGET_REPO")
	if repo == "" {
		return nil, fmt.Errorf("missing required env var: TARGET_REPO")
	}

	cfg := &Config{
		Providers: reg,
		Roles:     roles,
		Target: TargetConfig{
			Repo:    repo,
			BaseRef: getEnvDefault("TARGET_BASE_REF", "main"),
		},
		MaxRetries:            getEnvInt("MAX_RETRIES", 3),
		MaxParallel:           max1(getEnvInt("MAX_PARALLEL", 2)),
		DefaultVerifyCommands: splitNonEmpty(os.Getenv("HERO_DEFAULT_VERIFY"), "\n"),
		VerifyTimeout:         time.Duration(max1(getEnvInt("HERO_VERIFY_TIMEOUT_MS", 120_000))) * time.Millisecond,
	}
	return cfg, nil
}

// LLMFor resolves a role binding ("worker", "judge") into the connection
// LLMClient needs. Errors are explicit about which role asked for what.
func (c *Config) LLMFor(role string) (LLMConfig, error) {
	b, ok := c.Roles[role]
	if !ok {
		return LLMConfig{}, fmt.Errorf("role %q not configured", role)
	}
	p, err := c.Providers.Get(b.Provider, "HERO_"+strings.ToUpper(role))
	if err != nil {
		return LLMConfig{}, err
	}
	return LLMConfig{
		BaseURL:     p.BaseURL,
		APIKey:      p.APIKey,
		Model:       b.Model,
		InsecureTLS: p.InsecureTLS,
	}, nil
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
