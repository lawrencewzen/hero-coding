// Package config holds hero-coding runtime configuration.
//
// LLMConfig matches the shape that internal/agent.LLMClient consumes, so
// agent code lifted from upstream continues to compile unchanged. The
// InsecureTLS knob is a hero-coding addition for talking to local
// self-signed proxies (e.g. coproxy at https://localhost:...).
package config

// LLMConfig is the resolved per-call connection: provider connection
// info collapsed together with the role's chosen model.
//
// ReasoningEffort, when non-empty, is sent as the OpenAI-style
// `reasoning_effort` field on every chat request. Standard OpenAI values are
// "low" / "medium" / "high"; some providers (e.g. Ant Ling's Ring family)
// accept extended values like "xhigh". The field is forwarded verbatim — we
// don't validate it, so new provider-specific levels work without code change.
type LLMConfig struct {
	BaseURL         string `json:"base_url"`
	APIKey          string `json:"api_key"`
	Model           string `json:"model"`
	InsecureTLS     bool   `json:"insecure_tls,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}
