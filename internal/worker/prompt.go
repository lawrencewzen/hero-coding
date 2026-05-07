package worker

import (
	"strings"

	"hero-coding/internal/story"
)

// composeSystemPrompt appends a hard tool-policy block when the role
// restricts the toolset. The schema-level filter in worker.go is the
// primary enforcement; this prompt-level reminder helps the model
// self-police even on edge cases (e.g. when it would otherwise
// hallucinate a missing tool name).
func composeSystemPrompt(base string, allowedTools []string) string {
	if len(allowedTools) == 0 {
		return base
	}
	return base + "\n\nTOOL POLICY (hard constraint):\n" +
		"- You may ONLY use these tools: " + strings.Join(allowedTools, ", ") + ".\n" +
		"- Calling any other tool is a policy violation — the round will be killed and marked as FAIL.\n" +
		"- If you genuinely need a tool that is not on the list, surface the request in a final text message instead of calling it.\n"
}

// buildUserPrompt joins the story body with optional Captain feedback.
func buildUserPrompt(s *story.Story, captainFeedback string) string {
	if captainFeedback == "" {
		return s.Body
	}
	return strings.Join([]string{
		s.Body,
		"",
		"---",
		"Previous attempt was rejected by Judge. Address this in this round:",
		captainFeedback,
	}, "\n")
}
