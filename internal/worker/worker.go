// Package worker drives the worker LLM through a tool-using loop bounded
// by guardrails (max tool calls, wall-time, allowlist, loop detection).
package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"hero-coding/internal/agent"
	"hero-coding/internal/config"
	"hero-coding/internal/role"
	"hero-coding/internal/state"
	"hero-coding/internal/story"
	"hero-coding/internal/tooldef"
)

// MaxWallTime caps a single round's elapsed time. The loop returns with
// KillReason=wall_timeout if exceeded.
const MaxWallTime = 5 * time.Minute

// MaxIterations is a hard cap on chat→tool→chat cycles independent of
// tool-call count (every assistant turn counts as one iteration).
const MaxIterations = 200

// Options is the per-round input.
type Options struct {
	Story           *story.Story
	TargetRepo      string
	Round           int
	CaptainFeedback string

	// TraceDir, when set, gets a JSONL trace file per round.
	TraceDir string
}

// Worker holds the per-process LLM client and the Role it plays.
type Worker struct {
	llm  *agent.LLMClient
	role role.Role
}

// New constructs a Worker bound to a worker LLM connection. If role.Model
// is non-empty it overrides cfg.Model for this Worker.
func New(cfg config.LLMConfig, r role.Role) *Worker {
	if r.Model != "" {
		cfg.Model = r.Model
	}
	return &Worker{
		llm:  agent.NewLLMClient(&cfg),
		role: r,
	}
}

// Run executes one PEV round and returns the per-round stats.
func (w *Worker) Run(ctx context.Context, opts Options) (state.WorkerRunStats, error) {
	allowed := w.role.AllowedTools
	systemPrompt := composeSystemPrompt(w.role.SystemPrompt, allowed)
	userPrompt := buildUserPrompt(opts.Story, opts.CaptainFeedback)

	// Schema-level tool restriction: only tools in `allowed` are exposed to
	// the LLM. Disallowed tools physically do not exist in the schema the
	// model sees, so it cannot call them in the first place. Guardrails
	// remain as a defense-in-depth second line (in case a future change
	// widens the schema, or the model hallucinates a tool name).
	allowSet := map[string]struct{}{}
	for _, name := range allowed {
		allowSet[name] = struct{}{}
	}
	tools := buildTools(opts.TargetRepo)
	toolByName := map[string]tooldef.Tool{}
	toolDefs := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		td := t.Definition()
		toolByName[td.Function.Name] = t
		if _, ok := allowSet[td.Function.Name]; !ok && len(allowSet) > 0 {
			continue
		}
		raw, _ := json.Marshal(td)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		toolDefs = append(toolDefs, m)
	}

	tracer := openTrace(opts.TraceDir, opts.Story, opts.Round)
	defer tracer.Close()

	guard := newGuardrails(allowed)
	stats := state.WorkerRunStats{
		Round:         opts.Round,
		ToolUseByName: map[string]int{},
	}

	wallCtx, cancelWall := context.WithTimeout(ctx, MaxWallTime)
	defer cancelWall()

	msgs := []agent.ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}
	tracer.Event("user_prompt", map[string]any{"text": userPrompt})

	start := time.Now()

loop:
	for iter := 0; iter < MaxIterations; iter++ {
		if wallCtx.Err() != nil {
			stats.KillReason = KillWallTimeout
			break
		}
		resp, err := w.llm.Chat(wallCtx, msgs, toolDefs)
		if err != nil {
			if wallCtx.Err() != nil {
				stats.KillReason = KillWallTimeout
				break
			}
			stats.ExitCode = 1
			tracer.Event("chat_error", map[string]any{"error": err.Error()})
			return stats, fmt.Errorf("worker chat: %w", err)
		}
		if resp.Usage != nil {
			stats.TokensIn += resp.Usage.PromptTokens
			stats.TokensOut += resp.Usage.CompletionTokens
		}
		tracer.Event("assistant_message", map[string]any{
			"finish_reason": resp.FinishReason,
			"tool_calls":    len(resp.ToolCalls),
			"text":          extractText(resp.Content),
		})
		msgs = append(msgs, *resp)

		if len(resp.ToolCalls) == 0 {
			break loop
		}

		for _, tc := range resp.ToolCalls {
			args := decodeArgs(tc.Function.Arguments)
			ev := toolEvent{Name: tc.Function.Name, Args: args}
			if reason := guard.observe(ev); reason != "" {
				stats.KillReason = reason
				tracer.Event("guardrail_kill", map[string]any{"reason": reason, "tool": tc.Function.Name})
				break loop
			}
			stats.ToolUseByName[tc.Function.Name]++
			stats.ToolUseTotal++

			tool, ok := toolByName[tc.Function.Name]
			var result string
			if !ok {
				result = fmt.Sprintf("error: unknown tool %q", tc.Function.Name)
			} else {
				tracer.Event("tool_call", map[string]any{"name": tc.Function.Name, "args": args})
				out, terr := tool.Execute(wallCtx, args)
				if terr != nil {
					result = "error: " + terr.Error()
				} else {
					result = out
				}
				tracer.Event("tool_result", map[string]any{"name": tc.Function.Name, "ok": terr == nil, "result": truncate(result, 500)})
			}
			msgs = append(msgs, agent.ChatMessage{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    result,
			})
		}
	}

	stats.WallMs = time.Since(start).Milliseconds()
	tracer.Event("round_end", map[string]any{
		"wall_ms":      stats.WallMs,
		"tool_total":   stats.ToolUseTotal,
		"kill_reason":  stats.KillReason,
		"tokens_in":    stats.TokensIn,
		"tokens_out":   stats.TokensOut,
	})
	return stats, nil
}

func decodeArgs(raw string) map[string]any {
	if raw == "" {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return map[string]any{}
	}
	return m
}

func extractText(content any) string {
	switch v := content.(type) {
	case nil:
		return ""
	case string:
		return v
	case []agent.ContentPart:
		out := ""
		for _, p := range v {
			if p.Type == "text" || p.Type == "" {
				out += p.Text
			}
		}
		return out
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// trace is a JSONL writer per round. Cheap, append-only, easy to grep.
type trace struct {
	w  io.WriteCloser
	mu sync.Mutex
}

func openTrace(dir string, s *story.Story, round int) *trace {
	if dir == "" {
		return &trace{}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return &trace{}
	}
	fp := filepath.Join(dir, fmt.Sprintf("%s-r%d-%d.jsonl", s.ID(), round, time.Now().UnixMilli()))
	f, err := os.Create(fp)
	if err != nil {
		return &trace{}
	}
	return &trace{w: f}
}

func (t *trace) Event(kind string, data map[string]any) {
	if t == nil || t.w == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	rec := map[string]any{"ts": time.Now().UnixMilli(), "kind": kind}
	for k, v := range data {
		rec[k] = v
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	_, _ = t.w.Write(append(b, '\n'))
}

func (t *trace) Close() error {
	if t == nil || t.w == nil {
		return nil
	}
	return t.w.Close()
}
