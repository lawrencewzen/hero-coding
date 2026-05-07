// Package config holds hero-coding runtime configuration. The LLMConfig
// struct here matches the shape that internal/agent.LLMClient expects, so
// agent code lifted from upstream continues to compile unchanged.
package config

// LLMConfig is the minimum a chat-completions client needs.
type LLMConfig struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
}
