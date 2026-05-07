// Package config holds hero-coding runtime configuration.
//
// LLMConfig matches the shape that internal/agent.LLMClient consumes, so
// agent code lifted from upstream continues to compile unchanged. The
// InsecureTLS knob is a hero-coding addition for talking to local
// self-signed proxies (e.g. coproxy at https://localhost:...).
package config

// LLMConfig is the resolved per-call connection: provider connection
// info collapsed together with the role's chosen model.
type LLMConfig struct {
	BaseURL     string `json:"base_url"`
	APIKey      string `json:"api_key"`
	Model       string `json:"model"`
	InsecureTLS bool   `json:"insecure_tls,omitempty"`
}
