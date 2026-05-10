package story

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// safeID guards the `id` field — it is used verbatim as a git branch suffix
// (`hero/<id>`) and as a path component for state and worktree directories,
// so it must avoid characters git refs / filesystems reject.
var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Priority is a fixed enum mirroring the TS schema.
type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityNormal Priority = "normal"
	PriorityHigh   Priority = "high"
)

// VerifyTier is one stage of the layered verifier pipeline. Tiers run in
// declaration order; commands within a tier all execute, but if any tier
// has a failing command, subsequent tiers are skipped (fail-fast across
// tiers, run-to-completion within a tier — same as Codex / Augment).
//
// The default name "default" is used when a story declares verify as a
// flat list, so a single-tier setup is just a list with no map syntax.
type VerifyTier struct {
	Name     string
	Commands []string
}

// Frontmatter is the typed form of the story's YAML header.
type Frontmatter struct {
	ID         string       `yaml:"id"`
	Title      string       `yaml:"title"`
	Created    string       // populated from rawCreated (string or time.Time)
	Priority   Priority     `yaml:"priority"`
	MaxRetries *int         `yaml:"max_retries"`
	Verify     []VerifyTier // parsed from either a flat list or an ordered map
	Scope      []string     `yaml:"scope"`
	DependsOn  []string     `yaml:"depends_on"` // sequencer uses this for DAG ordering
}

// rawFrontmatter is the wire-form used during YAML decoding. `Verify` is a
// raw yaml.Node so we can dispatch on Kind (sequence = flat list,
// mapping = ordered tier map). `Created` accepts string or time.Time.
type rawFrontmatter struct {
	ID         string    `yaml:"id"`
	Title      string    `yaml:"title"`
	Created    any       `yaml:"created"`
	Priority   string    `yaml:"priority"`
	MaxRetries *int      `yaml:"max_retries"`
	Verify     yaml.Node `yaml:"verify"`
	Scope      []string  `yaml:"scope"`
	DependsOn  []string  `yaml:"depends_on"`
}

// Story is a parsed user story file.
type Story struct {
	Filepath    string
	Frontmatter Frontmatter
	Body        string
	Raw         string
}

// ID returns the story's canonical identifier (frontmatter id, or filename
// stem as a fallback). Caller code should use this everywhere a story is
// referenced by short name.
func (s *Story) ID() string {
	if s.Frontmatter.ID != "" {
		return s.Frontmatter.ID
	}
	return strings.TrimSuffix(filepath.Base(s.Filepath), ".md")
}

// Parse reads and validates a story file.
func Parse(filepath string) (*Story, error) {
	raw, err := os.ReadFile(filepath)
	if err != nil {
		return nil, fmt.Errorf("read story: %w", err)
	}
	fm, body, err := splitFrontmatter(raw)
	if err != nil {
		return nil, err
	}

	var rfm rawFrontmatter
	if err := yaml.Unmarshal(fm, &rfm); err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}

	parsed, err := normalize(rfm)
	if err != nil {
		return nil, err
	}

	return &Story{
		Filepath:    filepath,
		Frontmatter: parsed,
		Body:        strings.TrimSpace(body),
		Raw:         string(raw),
	}, nil
}

func normalize(r rawFrontmatter) (Frontmatter, error) {
	if r.ID == "" {
		return Frontmatter{}, fmt.Errorf("frontmatter.id is required")
	}
	if !safeID.MatchString(r.ID) {
		return Frontmatter{}, fmt.Errorf("frontmatter.id %q must match %s", r.ID, safeID.String())
	}
	if r.Title == "" {
		return Frontmatter{}, fmt.Errorf("frontmatter.title is required")
	}

	prio := Priority(r.Priority)
	switch prio {
	case "":
		prio = PriorityNormal
	case PriorityLow, PriorityNormal, PriorityHigh:
	default:
		return Frontmatter{}, fmt.Errorf("frontmatter.priority %q invalid (low|normal|high)", r.Priority)
	}

	if r.MaxRetries != nil && *r.MaxRetries < 0 {
		return Frontmatter{}, fmt.Errorf("frontmatter.max_retries must be non-negative")
	}
	verify, err := parseVerify(r.Verify)
	if err != nil {
		return Frontmatter{}, fmt.Errorf("frontmatter.verify: %w", err)
	}
	for i, v := range r.Scope {
		if strings.TrimSpace(v) == "" {
			return Frontmatter{}, fmt.Errorf("frontmatter.scope[%d] must be non-empty", i)
		}
	}

	created := ""
	switch v := r.Created.(type) {
	case nil:
	case string:
		created = v
	case time.Time:
		created = v.UTC().Format(time.RFC3339)
	default:
		return Frontmatter{}, fmt.Errorf("frontmatter.created must be a string or date, got %T", v)
	}

	for i, d := range r.DependsOn {
		if d == "" {
			return Frontmatter{}, fmt.Errorf("frontmatter.depends_on[%d] must be non-empty", i)
		}
		if !safeID.MatchString(d) {
			return Frontmatter{}, fmt.Errorf("frontmatter.depends_on[%d] %q must match %s", i, d, safeID.String())
		}
		if d == r.ID {
			return Frontmatter{}, fmt.Errorf("frontmatter.depends_on[%d]: story cannot depend on itself", i)
		}
	}

	return Frontmatter{
		ID:         r.ID,
		Title:      r.Title,
		Created:    created,
		Priority:   prio,
		MaxRetries: r.MaxRetries,
		Verify:     verify,
		Scope:      r.Scope,
		DependsOn:  r.DependsOn,
	}, nil
}

// parseVerify accepts either:
//   - a flat YAML list of commands: ["go test ./...", "go vet ./..."]
//     → wrapped into a single tier named "default"
//   - an ordered YAML map of tier name → commands:
//     {typecheck: [...], lint: [...], unit: [...]}
//     → preserved in declaration order
//
// Empty / missing verify returns nil so the dispatcher can decide whether
// to fall back to default_verify in roles.yaml.
func parseVerify(n yaml.Node) ([]VerifyTier, error) {
	if n.Kind == 0 {
		return nil, nil
	}
	switch n.Kind {
	case yaml.SequenceNode:
		var cmds []string
		if err := n.Decode(&cmds); err != nil {
			return nil, fmt.Errorf("must be a list of strings: %w", err)
		}
		if len(cmds) == 0 {
			return nil, nil
		}
		for i, c := range cmds {
			if strings.TrimSpace(c) == "" {
				return nil, fmt.Errorf("[%d] must be non-empty", i)
			}
		}
		return []VerifyTier{{Name: "default", Commands: cmds}}, nil
	case yaml.MappingNode:
		// yaml.Node.Content for mappings is [key0, val0, key1, val1, ...] in
		// declaration order, which is exactly what we need for tier ordering.
		tiers := make([]VerifyTier, 0, len(n.Content)/2)
		for i := 0; i < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			if key.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("tier name at position %d must be a string", i/2)
			}
			name := strings.TrimSpace(key.Value)
			if name == "" {
				return nil, fmt.Errorf("tier name at position %d is empty", i/2)
			}
			var cmds []string
			if err := val.Decode(&cmds); err != nil {
				return nil, fmt.Errorf("tier %q: must be a list of strings: %w", name, err)
			}
			if len(cmds) == 0 {
				return nil, fmt.Errorf("tier %q: must have at least one command", name)
			}
			for j, c := range cmds {
				if strings.TrimSpace(c) == "" {
					return nil, fmt.Errorf("tier %q [%d] must be non-empty", name, j)
				}
			}
			tiers = append(tiers, VerifyTier{Name: name, Commands: cmds})
		}
		return tiers, nil
	default:
		return nil, fmt.Errorf("must be a list of commands or a map of tiered command lists")
	}
}

// splitFrontmatter splits a YAML-frontmatter markdown file into the YAML
// block and the body. Files without a leading `---` line are treated as
// pure body with no frontmatter (an error since id is required).
func splitFrontmatter(raw []byte) ([]byte, string, error) {
	const sep = "---"
	// Tolerate UTF-8 BOM and CRLF.
	r := bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	r = bytes.ReplaceAll(r, []byte("\r\n"), []byte("\n"))

	if !bytes.HasPrefix(r, []byte(sep+"\n")) && !bytes.HasPrefix(r, []byte(sep+" \n")) {
		return nil, "", fmt.Errorf("missing YAML frontmatter (file must start with `---`)")
	}
	rest := r[len(sep)+1:]
	end := bytes.Index(rest, []byte("\n"+sep))
	if end < 0 {
		return nil, "", fmt.Errorf("unterminated YAML frontmatter (missing closing `---`)")
	}
	fm := rest[:end]
	body := rest[end+len("\n"+sep):]
	body = bytes.TrimPrefix(body, []byte("\n"))
	return fm, string(body), nil
}

// AppendReviewerFeedback appends a CHANGES_REQUESTED block for `round` to
// the story file under a single "Reviewer Feedback (auto)" section.
// Idempotent: if the same round marker is already present, no write happens.
// Atomic: write goes to a sibling .tmp file, then renamed.
func AppendReviewerFeedback(filepath string, round int, reason string) error {
	rawBytes, err := os.ReadFile(filepath)
	if err != nil {
		return err
	}
	raw := string(rawBytes)
	marker := fmt.Sprintf("### Round %d — CHANGES_REQUESTED", round)
	if strings.Contains(raw, marker) {
		return nil
	}
	const sep = "\n\n## Reviewer Feedback (auto)\n"
	block := fmt.Sprintf("\n%s\n%s\n", marker, reason)
	var next string
	if strings.Contains(raw, sep) {
		next = raw + block
	} else {
		next = raw + sep + block
	}
	tmp := filepath + ".tmp"
	if err := os.WriteFile(tmp, []byte(next), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath)
}
