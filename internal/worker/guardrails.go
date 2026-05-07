package worker

import (
	"encoding/json"
	"fmt"
)

// Kill reasons reported in WorkerRunStats.KillReason.
const (
	KillLoop          = "loop"
	KillToolCap       = "tool_cap"
	KillWallTimeout   = "wall_timeout"
	KillToolViolation = "tool_violation"
)

const (
	MaxToolCalls       = 80
	loopDetectWindow   = 6
	loopDetectThresh   = 4
)

type toolEvent struct {
	Name string
	Args map[string]any
}

func toolSig(ev toolEvent) string {
	b, _ := json.Marshal(ev.Args)
	return fmt.Sprintf("%s|%s", ev.Name, string(b))
}

type guardrails struct {
	recent        []string
	toolTotal     int
	allowed       map[string]struct{}
	lastViolation string
}

func newGuardrails(allowedTools []string) *guardrails {
	g := &guardrails{}
	if len(allowedTools) > 0 {
		g.allowed = make(map[string]struct{}, len(allowedTools))
		for _, t := range allowedTools {
			g.allowed[t] = struct{}{}
		}
	}
	return g
}

// observe is called per tool call BEFORE execution. Returns a non-empty
// kill reason when the round must be aborted.
func (g *guardrails) observe(ev toolEvent) string {
	g.toolTotal++
	if g.toolTotal > MaxToolCalls {
		return KillToolCap
	}
	if g.allowed != nil {
		if _, ok := g.allowed[ev.Name]; !ok {
			g.lastViolation = ev.Name
			return KillToolViolation
		}
	}
	sig := toolSig(ev)
	g.recent = append(g.recent, sig)
	if len(g.recent) > loopDetectWindow {
		g.recent = g.recent[1:]
	}
	n := 0
	for _, s := range g.recent {
		if s == sig {
			n++
		}
	}
	if n >= loopDetectThresh {
		return KillLoop
	}
	return ""
}
