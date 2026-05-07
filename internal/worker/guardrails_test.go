package worker

import "testing"

func TestGuardrails_NormalUsage(t *testing.T) {
	g := newGuardrails(nil)
	if r := g.observe(toolEvent{Name: "bash", Args: map[string]any{"cmd": "ls"}}); r != "" {
		t.Fatalf("expected empty kill, got %q", r)
	}
	if r := g.observe(toolEvent{Name: "read_file", Args: map[string]any{"path": "a"}}); r != "" {
		t.Fatalf("expected empty kill, got %q", r)
	}
}

func TestGuardrails_LoopDetect(t *testing.T) {
	g := newGuardrails(nil)
	ev := toolEvent{Name: "bash", Args: map[string]any{"cmd": "git status"}}
	for i := 0; i < 3; i++ {
		if r := g.observe(ev); r != "" {
			t.Fatalf("iter %d expected empty, got %q", i, r)
		}
	}
	if r := g.observe(ev); r != KillLoop {
		t.Fatalf("expected loop, got %q", r)
	}
}

func TestGuardrails_ToolCap(t *testing.T) {
	g := newGuardrails(nil)
	var last string
	for i := 0; i < MaxToolCalls+1; i++ {
		last = g.observe(toolEvent{Name: "read_file", Args: map[string]any{"path": "f"}})
	}
	if last != KillToolCap {
		t.Fatalf("expected tool_cap, got %q", last)
	}
}

func TestGuardrails_AllowlistViolation(t *testing.T) {
	g := newGuardrails([]string{"read_file", "edit_file", "bash"})
	if r := g.observe(toolEvent{Name: "read_file", Args: map[string]any{}}); r != "" {
		t.Fatalf("expected empty, got %q", r)
	}
	if r := g.observe(toolEvent{Name: "fetch_url", Args: map[string]any{"u": "x"}}); r != KillToolViolation {
		t.Fatalf("expected tool_violation, got %q", r)
	}
	if g.lastViolation != "fetch_url" {
		t.Fatalf("expected lastViolation=fetch_url, got %q", g.lastViolation)
	}
}

func TestGuardrails_EmptyAllowlistDoesNotRestrict(t *testing.T) {
	g := newGuardrails(nil)
	if r := g.observe(toolEvent{Name: "anything", Args: map[string]any{}}); r != "" {
		t.Fatalf("expected empty, got %q", r)
	}
}
