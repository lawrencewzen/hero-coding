// Package role models the configurable behavior of one agent slot in a
// PEV (Plan-Execute-Verify) pipeline. The same Agent runtime backs every
// slot; what differs is the system prompt, the tool whitelist (enforced
// schema-level), and an optional per-slot model override.
//
// This mirrors the "single parameterized MainAgent + multiple Roles"
// pattern from arxiv:2603.05344 — variation comes from configuration,
// not from a class hierarchy.
package role

// Role describes one slot's behavior.
type Role struct {
	// Name is a short identifier ("worker", "verifier", "judge"). Used
	// only for logging and tracing.
	Name string

	// SystemPrompt is the role-specific prompt prepended to every chat.
	// May be empty for non-LLM roles (e.g. Verifier).
	SystemPrompt string

	// AllowedTools is the schema-level whitelist of tool names the role
	// may use. An empty slice means "no tools" (e.g. Judge); nil means
	// "no restriction" (escape hatch — avoid in production roles).
	AllowedTools []string

	// Model, when non-empty, overrides the connection's default model
	// for this role. "" = use whatever the LLMConfig already specified.
	Model string
}

// Roles bundles the standard PEV trio so callers don't need to know
// the names individually.
type Roles struct {
	Worker   Role
	Verifier Role
	Judge    Role
}

const workerSystemPrompt = `You are Worker — a focused, single-task coding agent.

You are given one user story. Read the acceptance criteria, work the codebase, finish.

GIT COMMIT RULE (mandatory, the most important rule):
- After every meaningful edit/write that brings the code closer to acceptance, you MUST run ` + "`git add`" + ` and ` + "`git commit`" + ` BEFORE any further reasoning or other tool call.
- A round where files are modified but no ` + "`git commit`" + ` runs is treated as FAIL — the Judge only sees commits, not your scratch edits.
- Use Conventional Commit messages. Each commit message MUST explain WHY in one short line, then list WHAT changed in bullets, then mention any errors fixed since the previous commit.
- Make commits atomic: one logical change per commit.

Other hard rules:
- Run tests / type checks before declaring done. If they fail, fix and commit again.
- Do not modify files outside the explicit scope of the story.
- Stop and surface the blocker if a step fails three times in a row — do not loop.

STOP CONDITION (read carefully):
When the acceptance criteria are met AND ` + "`git status --short`" + ` shows a clean tree AND ` + "`git log`" + ` contains your new commits AND tests pass:
  1. Write a final summary message in one assistant turn (text only, no tool calls).
  2. Then STOP. Do NOT call any more tools. Do NOT echo "done" or "all good" via bash.
  3. Calling another tool after a clean-and-committed success will be treated as a bug.
`

const judgeSystemPrompt = `You are Captain — a strict but fair code reviewer in a Plan-Execute-Verify (PEV) pipeline.

You receive:
1. The user story (Goal + Acceptance Criteria + Constraints).
2. Verifier results — deterministic command runs (tests / lint / typecheck). The Verifier is authoritative on its checks; do not second-guess green/red.
3. Git history since base (commits + full diff).

Decide:
- PASS only if (a) every Acceptance Criterion is clearly met by the diffs, (b) Constraints / Out of Scope are respected, and (c) every Verifier command exited 0 (or no Verifier was run).
- FAIL if any Verifier command failed, OR an Acceptance Criterion is not visibly satisfied, OR scope was violated.

In FAIL, give one paragraph of concrete, actionable feedback the next worker round can address. Quote the failing command name and a short error excerpt when the Verifier was red. Reference specific files / commits when relevant.

Do not pass on diff aesthetics alone. Deterministic evidence outranks vibes.

Reply with ONLY a single JSON object, no prose, no code fences:
{"verdict": "PASS" | "FAIL", "reason": string}
`

// Default whitelist for Worker: filesystem-as-context tools per Microsoft
// Azure SRE finding (45% → 75% Intent Met when bash/read/edit/write/grep/ls
// replaces 100+ specialized tools), plus bash because git commit is mandatory.
var defaultWorkerTools = []string{"bash", "read_file", "edit_file", "write_file", "grep", "ls"}

// Defaults returns the standard Roles. Callers may override individual
// fields (e.g. swap in a different system prompt or restrict a smaller
// tool set) before handing them to the dispatcher.
func Defaults() Roles {
	return Roles{
		Worker: Role{
			Name:         "worker",
			SystemPrompt: workerSystemPrompt,
			AllowedTools: append([]string(nil), defaultWorkerTools...),
		},
		Verifier: Role{
			Name: "verifier",
			// Verifier runs deterministic shell commands declared by the
			// story; it has no LLM and therefore no prompt or tools.
		},
		Judge: Role{
			Name:         "judge",
			SystemPrompt: judgeSystemPrompt,
			AllowedTools: []string{}, // Judge writes a verdict, never calls tools.
		},
	}
}
