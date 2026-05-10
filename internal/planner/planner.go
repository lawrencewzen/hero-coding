// Package planner turns a high-level Markdown plan into a sequence of
// independently-verifiable user stories that the dispatcher can run.
//
// The Planner is invoked once per project plan (typically via
// `hero plan <plan.md>`), produces N story files plus a manifest into
// inbox/<plan-name>/, and exits. It is NOT part of the per-round PEV
// loop — Planner output is durable artefacts, reviewable by the user
// before any worker runs.
package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"hero-coding/internal/agent"
	"hero-coding/internal/config"
	"hero-coding/internal/role"
)

// Options is the per-call input to Run.
type Options struct {
	PlanPath  string            // Path to the Markdown plan file.
	OutputDir string            // Where to write inbox/<plan-name>/. Created if missing.
	Planner   config.LLMConfig  // LLM connection, including default reasoning_effort.
	Role      role.Role         // SystemPrompt drives the prompt; Model overrides Planner.Model when set.
}

// Result describes the artefacts the Planner emitted.
type Result struct {
	PlanName     string
	StoryFiles   []string // absolute paths of every story md written
	ManifestPath string   // absolute path of _manifest.yaml
}

// Run reads the plan at opts.PlanPath, calls the Planner LLM, and writes
// one story file per planned user story under opts.OutputDir/<plan-name>/.
// The plan-name comes from the LLM output; OutputDir is the parent
// directory (typically the project's `inbox/`).
func Run(ctx context.Context, opts Options) (Result, error) {
	planBytes, err := os.ReadFile(opts.PlanPath)
	if err != nil {
		return Result{}, fmt.Errorf("read plan: %w", err)
	}

	cfg := opts.Planner
	if opts.Role.Model != "" {
		cfg.Model = opts.Role.Model
	}
	client := agent.NewLLMClient(&cfg)
	temp := 0.2
	jsonObj := "json_object"
	chatOpts := agent.ChatOptions{
		Temperature:    &temp,
		ResponseFormat: &jsonObj,
	}
	msgs := []agent.ChatMessage{
		{Role: "system", Content: opts.Role.SystemPrompt},
		{Role: "user", Content: "# Plan\n\n" + string(planBytes)},
	}

	resp, err := client.ChatWithOptions(ctx, msgs, nil, chatOpts)
	if err != nil {
		return Result{}, fmt.Errorf("planner llm: %w", err)
	}

	parsed, raw, err := parseStories(resp)
	if err != nil {
		return Result{}, fmt.Errorf("planner output: %w (raw: %s)", err, truncate(raw, 500))
	}
	if err := validateStories(parsed); err != nil {
		return Result{}, fmt.Errorf("planner output validation: %w", err)
	}

	planName := slugify(parsed.Plan)
	if planName == "" {
		// Fall back to the plan file's basename (without extension).
		planName = slugify(strings.TrimSuffix(filepath.Base(opts.PlanPath), filepath.Ext(opts.PlanPath)))
	}
	if planName == "" {
		planName = "plan"
	}
	dir := filepath.Join(opts.OutputDir, planName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("mkdir %s: %w", dir, err)
	}

	storyFiles := make([]string, 0, len(parsed.Stories))
	for _, s := range parsed.Stories {
		path := filepath.Join(dir, s.ID+".md")
		md, err := renderStory(s)
		if err != nil {
			return Result{}, fmt.Errorf("render %s: %w", s.ID, err)
		}
		if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
			return Result{}, fmt.Errorf("write %s: %w", path, err)
		}
		storyFiles = append(storyFiles, path)
	}

	manifestPath := filepath.Join(dir, "_manifest.yaml")
	if err := writeManifest(manifestPath, planName, parsed.Stories); err != nil {
		return Result{}, fmt.Errorf("write manifest: %w", err)
	}

	return Result{
		PlanName:     planName,
		StoryFiles:   storyFiles,
		ManifestPath: manifestPath,
	}, nil
}

// ---------------------------------------------------------------------------
// LLM output schema
// ---------------------------------------------------------------------------

type plannerOutput struct {
	Plan    string         `json:"plan"`
	Stories []storyOutput `json:"stories"`
}

type storyOutput struct {
	ID         string              `json:"id"`
	Title      string              `json:"title"`
	Priority   string              `json:"priority,omitempty"`
	Verify     map[string][]string `json:"verify,omitempty"`
	Scope      []string            `json:"scope,omitempty"`
	DependsOn  []string            `json:"depends_on"`
	Body       string              `json:"body"`
}

func parseStories(msg *agent.ChatMessage) (plannerOutput, string, error) {
	raw := strings.TrimSpace(extractText(msg.Content))
	if raw == "" {
		return plannerOutput{}, raw, fmt.Errorf("empty response")
	}
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var out plannerOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return plannerOutput{}, raw, err
	}
	return out, raw, nil
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func validateStories(p plannerOutput) error {
	if len(p.Stories) == 0 {
		return fmt.Errorf("no stories produced")
	}
	ids := make(map[string]bool, len(p.Stories))
	for _, s := range p.Stories {
		if !safeID.MatchString(s.ID) {
			return fmt.Errorf("invalid story id %q (must match %s)", s.ID, safeID.String())
		}
		if ids[s.ID] {
			return fmt.Errorf("duplicate story id %q", s.ID)
		}
		ids[s.ID] = true
		if strings.TrimSpace(s.Title) == "" {
			return fmt.Errorf("story %q: title is required", s.ID)
		}
		if strings.TrimSpace(s.Body) == "" {
			return fmt.Errorf("story %q: body is required", s.ID)
		}
	}
	// Validate depends_on references exist + no cycles.
	for _, s := range p.Stories {
		for _, dep := range s.DependsOn {
			if !ids[dep] {
				return fmt.Errorf("story %q references unknown dependency %q", s.ID, dep)
			}
			if dep == s.ID {
				return fmt.Errorf("story %q depends on itself", s.ID)
			}
		}
	}
	if cyc := findCycle(p.Stories); cyc != "" {
		return fmt.Errorf("dependency cycle detected: %s", cyc)
	}
	// Require at least one root (depends_on == []).
	hasRoot := false
	for _, s := range p.Stories {
		if len(s.DependsOn) == 0 {
			hasRoot = true
			break
		}
	}
	if !hasRoot {
		return fmt.Errorf("no root story (every story has a depends_on; need at least one independent entry point)")
	}
	return nil
}

// findCycle returns "" when the DAG is acyclic; otherwise a descriptive
// string of one offending cycle. Uses iterative DFS with three colours.
func findCycle(stories []storyOutput) string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	colour := map[string]int{}
	deps := map[string][]string{}
	for _, s := range stories {
		colour[s.ID] = white
		deps[s.ID] = append([]string(nil), s.DependsOn...)
	}

	var path []string
	var dfs func(id string) string
	dfs = func(id string) string {
		colour[id] = gray
		path = append(path, id)
		for _, d := range deps[id] {
			switch colour[d] {
			case gray:
				return strings.Join(append(path, d), " → ")
			case white:
				if c := dfs(d); c != "" {
					return c
				}
			}
		}
		colour[id] = black
		path = path[:len(path)-1]
		return ""
	}
	// Iterate in stable order so error messages are deterministic.
	ids := make([]string, 0, len(stories))
	for _, s := range stories {
		ids = append(ids, s.ID)
	}
	for _, id := range ids {
		if colour[id] == white {
			if c := dfs(id); c != "" {
				return c
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Rendering: storyOutput → Markdown story file
// ---------------------------------------------------------------------------

type storyFrontmatter struct {
	ID         string              `yaml:"id"`
	Title      string              `yaml:"title"`
	Priority   string              `yaml:"priority,omitempty"`
	Verify     map[string][]string `yaml:"verify,omitempty"`
	Scope      []string            `yaml:"scope,omitempty"`
	DependsOn  []string            `yaml:"depends_on"`
}

func renderStory(s storyOutput) (string, error) {
	fm := storyFrontmatter{
		ID:        s.ID,
		Title:     s.Title,
		Priority:  s.Priority,
		Verify:    s.Verify,
		Scope:     s.Scope,
		DependsOn: append([]string{}, s.DependsOn...),
	}
	if fm.Priority == "" {
		fm.Priority = "normal"
	}
	if fm.DependsOn == nil {
		fm.DependsOn = []string{}
	}

	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(fm); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return "---\n" + buf.String() + "---\n\n" + strings.TrimRight(s.Body, "\n") + "\n", nil
}

// ---------------------------------------------------------------------------
// Manifest: an at-a-glance index of the plan's stories + dependency edges
// ---------------------------------------------------------------------------

type manifestEntry struct {
	ID        string   `yaml:"id"`
	Title     string   `yaml:"title"`
	DependsOn []string `yaml:"depends_on"`
}

type manifestDoc struct {
	Plan    string          `yaml:"plan"`
	Created string          `yaml:"created"`
	Stories []manifestEntry `yaml:"stories"`
}

func writeManifest(path, planName string, stories []storyOutput) error {
	entries := make([]manifestEntry, 0, len(stories))
	for _, s := range stories {
		dep := append([]string{}, s.DependsOn...)
		if dep == nil {
			dep = []string{}
		}
		entries = append(entries, manifestEntry{ID: s.ID, Title: s.Title, DependsOn: dep})
	}
	doc := manifestDoc{
		Plan:    planName,
		Created: time.Now().UTC().Format(time.RFC3339),
		Stories: entries,
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

var slugUnsafe = regexp.MustCompile(`[^a-z0-9-]+`)
var slugDashes = regexp.MustCompile(`-+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugUnsafe.ReplaceAllString(s, "-")
	s = slugDashes.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

func extractText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []agent.ContentPart:
		var b strings.Builder
		for _, p := range v {
			if p.Type == "text" || p.Type == "" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	default:
		data, _ := json.Marshal(v)
		return string(data)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// keep sort referenced — used by tests for stable manifest comparison.
var _ = sort.Strings
