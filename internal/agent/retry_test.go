package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"hero-coding/internal/config"
)

// rateLimitThenSucceed returns a server that responds 429 for the first
// `failures` requests, then 200 with a minimal valid completion. The atomic
// counter lets tests assert the exact number of attempts the client made.
func rateLimitThenSucceed(t *testing.T, failures int32) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var seen atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := seen.Add(1)
		if n <= failures {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprintf(w, `{"error":{"message":"rate_limit attempt %d"}}`, n)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestRetryRateLimit_NoErrorPassesThrough(t *testing.T) {
	calls := 0
	out, err := retryRateLimit(context.Background(), func() (string, error) {
		calls++
		return "ok", nil
	})
	if err != nil || out != "ok" {
		t.Fatalf("got (%q, %v), want (\"ok\", nil)", out, err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestRetryRateLimit_NonRetryableErrorPassesThrough(t *testing.T) {
	want := errors.New("boom")
	calls := 0
	_, err := retryRateLimit(context.Background(), func() (int, error) {
		calls++
		return 0, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
	if calls != 1 {
		t.Errorf("non-retryable should not retry; got %d calls", calls)
	}
}

func TestChat_RecoversFromTransientRateLimit(t *testing.T) {
	// Server fails twice with 429, succeeds on the third. The default retry
	// budget tolerates 4 sleeps, so this MUST succeed.
	srv, seen := rateLimitThenSucceed(t, 2)
	c := NewLLMClient(&config.LLMConfig{BaseURL: srv.URL, APIKey: "k", Model: "m"})

	// Use a tight context to keep test fast — backoff is real (2s, 4s).
	// We wait long enough for the first two backoffs (~6s) plus slack.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	resp, err := c.Chat(ctx, []ChatMessage{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat after 2 transient 429s: %v", err)
	}
	if resp == nil || resp.Content != "ok" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if got := seen.Load(); got != 3 {
		t.Errorf("expected 3 server attempts, got %d", got)
	}
}

func TestChat_ContextCancelDuringBackoffStopsImmediately(t *testing.T) {
	// Server always 429s. Cancelling ctx during the first backoff should
	// abort with ctx.Err(), not exhaust the full retry budget.
	srv, seen := rateLimitThenSucceed(t, 999)
	c := NewLLMClient(&config.LLMConfig{BaseURL: srv.URL, APIKey: "k", Model: "m"})

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel ~500ms in — definitely during the first ~2s backoff.
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := c.Chat(ctx, []ChatMessage{{Role: "user", Content: "hi"}}, nil)
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("cancel should abort within ~1s, took %v", elapsed)
	}
	if got := seen.Load(); got != 1 {
		t.Errorf("expected 1 server attempt before cancel, got %d", got)
	}
}
