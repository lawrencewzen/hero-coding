// Package provider models a named LLM endpoint and the registry that
// resolves role bindings (e.g. "coproxy/gpt-5.4") into a concrete
// connection config.
//
// All configuration is environment-driven so the 12-factor flow
// (1Password CLI, direnv, k8s Secret, docker --env) Just Works:
//
//	HERO_PROVIDER_<name>_BASE_URL    OpenAI-compatible /v1 base URL
//	HERO_PROVIDER_<name>_API_KEY     bearer token
//	HERO_PROVIDER_<name>_INSECURE_TLS  "true" to skip TLS cert verify
//	                                  (dev-only — local self-signed proxies)
//
// Role bindings are short strings parsed by ParseBinding:
//
//	HERO_WORKER=coproxy/gpt-5.4
//	HERO_JUDGE=openai/gpt-5.5
package provider

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Provider holds the connection-level config for a single named endpoint.
// Per-role concerns (which model to use) live in the role binding, not here.
type Provider struct {
	Name        string
	BaseURL     string
	APIKey      string
	InsecureTLS bool
}

// Registry is the set of providers visible to the process.
type Registry map[string]Provider

// Binding is a parsed "<provider>/<model>" string from HERO_<ROLE>=...
type Binding struct {
	Provider string
	Model    string
}

// nameRe matches the provider name in HERO_PROVIDER_<name>_<field>.
// Allow letters, digits, underscore, dash, dot — same charset most env-var
// tooling tolerates inside the name slot.
var nameRe = regexp.MustCompile(`^HERO_PROVIDER_([A-Za-z0-9._-]+)_(BASE_URL|API_KEY|INSECURE_TLS)$`)

// LoadRegistry scans the process environment and returns every provider it
// finds. Each provider must have BASE_URL and API_KEY set; INSECURE_TLS is
// optional and defaults to false. An incomplete provider returns an error
// so misconfiguration fails loud at startup.
func LoadRegistry() (Registry, error) {
	type partial struct {
		baseURL     string
		apiKey      string
		insecureTLS string
		seen        bool
	}
	groups := map[string]*partial{}

	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		k, v := kv[:eq], kv[eq+1:]
		m := nameRe.FindStringSubmatch(k)
		if m == nil {
			continue
		}
		name, field := m[1], m[2]
		p, ok := groups[name]
		if !ok {
			p = &partial{}
			groups[name] = p
		}
		p.seen = true
		switch field {
		case "BASE_URL":
			p.baseURL = v
		case "API_KEY":
			p.apiKey = v
		case "INSECURE_TLS":
			p.insecureTLS = v
		}
	}

	out := make(Registry, len(groups))
	for name, p := range groups {
		if !p.seen {
			continue
		}
		if p.baseURL == "" {
			return nil, fmt.Errorf("provider %q: HERO_PROVIDER_%s_BASE_URL is required", name, name)
		}
		if p.apiKey == "" {
			return nil, fmt.Errorf("provider %q: HERO_PROVIDER_%s_API_KEY is required", name, name)
		}
		out[name] = Provider{
			Name:        name,
			BaseURL:     p.baseURL,
			APIKey:      p.apiKey,
			InsecureTLS: parseBool(p.insecureTLS),
		}
	}
	return out, nil
}

// ParseBinding splits "<provider>/<model>" into its parts. Whitespace is
// trimmed; both halves must be non-empty.
func ParseBinding(s string) (Binding, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Binding{}, fmt.Errorf("empty binding")
	}
	idx := strings.IndexByte(s, '/')
	if idx <= 0 || idx == len(s)-1 {
		return Binding{}, fmt.Errorf("binding %q must be <provider>/<model>", s)
	}
	return Binding{
		Provider: strings.TrimSpace(s[:idx]),
		Model:    strings.TrimSpace(s[idx+1:]),
	}, nil
}

// Get looks up a provider by name; returns an error referencing the role
// that asked for it so misconfiguration is easy to track down.
func (r Registry) Get(name, requestedBy string) (Provider, error) {
	p, ok := r[name]
	if !ok {
		known := make([]string, 0, len(r))
		for k := range r {
			known = append(known, k)
		}
		return Provider{}, fmt.Errorf(
			"provider %q (referenced by %s) not defined; known providers: %v",
			name, requestedBy, known,
		)
	}
	return p, nil
}

func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
