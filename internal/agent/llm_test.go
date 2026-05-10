package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"hero-coding/internal/config"
)

// captureRequest spins up a mock OpenAI-compatible server that records the
// last request body it received and returns a minimal valid completion.
func captureRequest(t *testing.T) (*httptest.Server, *map[string]any) {
	t.Helper()
	last := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &last)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &last
}

func TestChat_ReasoningEffort_FromConfig(t *testing.T) {
	srv, last := captureRequest(t)
	c := NewLLMClient(&config.LLMConfig{
		BaseURL:         srv.URL,
		APIKey:          "k",
		Model:           "ring-2.6-1t",
		ReasoningEffort: "xhigh",
	})
	if _, err := c.Chat(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := (*last)["reasoning_effort"]; got != "xhigh" {
		t.Fatalf("reasoning_effort: want xhigh, got %v", got)
	}
}

func TestChat_ReasoningEffort_OptsOverridesConfig(t *testing.T) {
	srv, last := captureRequest(t)
	c := NewLLMClient(&config.LLMConfig{
		BaseURL: srv.URL, APIKey: "k", Model: "m", ReasoningEffort: "high",
	})
	override := "xhigh"
	_, err := c.ChatWithOptions(
		context.Background(),
		[]ChatMessage{{Role: "user", Content: "hi"}},
		nil,
		ChatOptions{ReasoningEffort: &override},
	)
	if err != nil {
		t.Fatalf("ChatWithOptions: %v", err)
	}
	if got := (*last)["reasoning_effort"]; got != "xhigh" {
		t.Fatalf("override failed: want xhigh, got %v", got)
	}
}

func TestChat_ReasoningEffort_OptsEmptyStringSuppresses(t *testing.T) {
	srv, last := captureRequest(t)
	c := NewLLMClient(&config.LLMConfig{
		BaseURL: srv.URL, APIKey: "k", Model: "m", ReasoningEffort: "high",
	})
	suppress := ""
	_, err := c.ChatWithOptions(
		context.Background(),
		[]ChatMessage{{Role: "user", Content: "hi"}},
		nil,
		ChatOptions{ReasoningEffort: &suppress},
	)
	if err != nil {
		t.Fatalf("ChatWithOptions: %v", err)
	}
	if _, present := (*last)["reasoning_effort"]; present {
		t.Fatalf("expected reasoning_effort to be omitted, got %v", (*last)["reasoning_effort"])
	}
}

func TestChat_ReasoningEffort_OmittedWhenUnset(t *testing.T) {
	srv, last := captureRequest(t)
	c := NewLLMClient(&config.LLMConfig{BaseURL: srv.URL, APIKey: "k", Model: "m"})
	if _, err := c.Chat(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if _, present := (*last)["reasoning_effort"]; present {
		t.Fatalf("expected no reasoning_effort field, got %v", (*last)["reasoning_effort"])
	}
}

func TestStream_ReasoningEffort_FromConfig(t *testing.T) {
	srv, last := captureRequest(t)
	c := NewLLMClient(&config.LLMConfig{
		BaseURL: srv.URL, APIKey: "k", Model: "m", ReasoningEffort: "xhigh",
	})
	// Server returns plain JSON (not SSE), so Stream falls back to its
	// non-stream JSON path — fine for verifying the request body.
	if _, err := c.Stream(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got := (*last)["reasoning_effort"]; got != "xhigh" {
		t.Fatalf("stream reasoning_effort: want xhigh, got %v", got)
	}
	if got := (*last)["stream"]; got != true {
		t.Fatalf("expected stream=true in body, got %v", got)
	}
}
