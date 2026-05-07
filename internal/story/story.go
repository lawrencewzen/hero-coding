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

// Frontmatter is the typed form of the story's YAML header.
type Frontmatter struct {
	ID         string   `yaml:"id"`
	Title      string   `yaml:"title"`
	Created    string   `yaml:"-"` // populated from rawCreated below (string or time.Time)
	Priority   Priority `yaml:"priority"`
	MaxRetries *int     `yaml:"max_retries"`
	Verify     []string `yaml:"verify"`
	Scope      []string `yaml:"scope"`
}

// rawFrontmatter is the wire-form used during YAML decoding so we can accept
// either an ISO string or a YAML date for `created`.
type rawFrontmatter struct {
	ID         string   `yaml:"id"`
	Title      string   `yaml:"title"`
	Created    any      `yaml:"created"`
	Priority   string   `yaml:"priority"`
	MaxRetries *int     `yaml:"max_retries"`
	Verify     []string `yaml:"verify"`
	Scope      []string `yaml:"scope"`
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
	for i, v := range r.Verify {
		if strings.TrimSpace(v) == "" {
			return Frontmatter{}, fmt.Errorf("frontmatter.verify[%d] must be non-empty", i)
		}
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

	return Frontmatter{
		ID:         r.ID,
		Title:      r.Title,
		Created:    created,
		Priority:   prio,
		MaxRetries: r.MaxRetries,
		Verify:     r.Verify,
		Scope:      r.Scope,
	}, nil
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

// AppendJudgeFeedback appends a fail reason for `round` to the story file
// under a single "Captain Feedback (auto)" section. Idempotent: if the same
// round marker is already present, no write happens. Atomic: write goes to
// a sibling .tmp file, then renamed.
func AppendJudgeFeedback(filepath string, round int, reason string) error {
	rawBytes, err := os.ReadFile(filepath)
	if err != nil {
		return err
	}
	raw := string(rawBytes)
	marker := fmt.Sprintf("### Round %d — FAIL", round)
	if strings.Contains(raw, marker) {
		return nil
	}
	const sep = "\n\n## Captain Feedback (auto)\n"
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
