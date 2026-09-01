package codingeval

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

//go:embed catalog.json task.schema.json
var catalogFS embed.FS

type Catalog struct {
	Version int    `json:"version"`
	Tasks   []Task `json:"tasks"`
}

type Task struct {
	ID             string        `json:"id"`
	Title          string        `json:"title"`
	Description    string        `json:"description"`
	Prompt         string        `json:"prompt,omitempty"`
	Type           string        `json:"type"`
	Enabled        bool          `json:"enabled"`
	DisabledReason string        `json:"disabled_reason,omitempty"`
	Baseline       Baseline      `json:"baseline"`
	TimeoutSeconds int           `json:"timeout_seconds"`
	Fixture        Fixture       `json:"fixture"`
	Verify         []Command     `json:"verify"`
	Scoring        ScoringPolicy `json:"scoring"`
}

type Baseline struct {
	Included bool   `json:"included"`
	Reason   string `json:"reason"`
}

type Fixture struct {
	Files   map[string]string `json:"files"`
	Command string            `json:"command"`
}

type Command struct {
	Name    string `json:"name"`
	Command string `json:"command"`
}

type ScoringPolicy struct {
	ForbiddenPaths  []string `json:"forbidden_paths"`
	MaxChangedFiles int      `json:"max_changed_files"`
	MaxAddedLines   int      `json:"max_added_lines"`
	MaxDeletedLines int      `json:"max_deleted_lines"`
}

func (t Task) EffectivePrompt() string {
	if prompt := strings.TrimSpace(t.Prompt); prompt != "" {
		return prompt
	}
	if prompt := builtinSmokePrompts[t.ID]; prompt != "" {
		return prompt
	}
	return strings.TrimSpace(t.Description)
}

var builtinSmokePrompts = map[string]string{
	"smoke-write-greeting":   "Create greeting.txt containing exactly: hello coding eval followed by a newline. Do not modify other files.",
	"smoke-replace-config":   "In app.conf change only mode=dev to mode=test. Preserve the port and do not modify keep.txt.",
	"smoke-add-go-function":  "Add an exported Go function Answer() int to answer.go that returns 42. Validate the Go package.",
	"smoke-fix-typo":         "Fix the typo quik to quick in message.txt and preserve the second line unchanged.",
	"smoke-json-setting":     "Change settings.json enabled from false to true while preserving name=fixture and valid JSON.",
	"smoke-markdown-heading": "Prepend a level-one Markdown heading '# Notes', then one blank line, to notes.md. Preserve the existing note.",
	"smoke-generate-output":  "Create output.txt from input.txt with every lowercase letter converted to uppercase. Do not modify input.txt.",
	"smoke-delete-obsolete":  "Delete obsolete.txt and do not modify current.txt or any other file.",
	"smoke-nested-file":      "Change src/pkg/value.txt from old to new. Do not modify src/other/value.txt.",
	"smoke-unicode-content":  "Replace message.txt content with exactly '你好，Coding Alpha' followed by a newline, preserving UTF-8.",
}

func DefaultCatalog() (Catalog, error) {
	f, err := catalogFS.Open("catalog.json")
	if err != nil {
		return Catalog{}, err
	}
	defer f.Close()
	return DecodeCatalog(f)
}

func TaskSchema() ([]byte, error) {
	return catalogFS.ReadFile("task.schema.json")
}

func LoadCatalog(path string) (Catalog, error) {
	f, err := os.Open(path)
	if err != nil {
		return Catalog{}, err
	}
	defer f.Close()
	return DecodeCatalog(f)
}

func DecodeCatalog(r io.Reader) (Catalog, error) {
	var catalog Catalog
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&catalog); err != nil {
		return Catalog{}, fmt.Errorf("decode catalog: %w", err)
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return Catalog{}, fmt.Errorf("decode catalog: trailing JSON value")
	}
	if err := catalog.Validate(); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

func (c Catalog) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("catalog version must be 1")
	}
	if len(c.Tasks) == 0 {
		return fmt.Errorf("catalog must contain tasks")
	}
	seen := make(map[string]struct{}, len(c.Tasks))
	for i := range c.Tasks {
		if err := c.Tasks[i].Validate(); err != nil {
			return fmt.Errorf("task[%d]: %w", i, err)
		}
		if _, ok := seen[c.Tasks[i].ID]; ok {
			return fmt.Errorf("duplicate task id %q", c.Tasks[i].ID)
		}
		seen[c.Tasks[i].ID] = struct{}{}
	}
	return nil
}

func (t Task) Validate() error {
	if strings.TrimSpace(t.ID) == "" || strings.TrimSpace(t.Title) == "" || strings.TrimSpace(t.Description) == "" {
		return fmt.Errorf("id, title, and description are required")
	}
	if t.Type != "fixture" {
		return fmt.Errorf("%s: unsupported type %q", t.ID, t.Type)
	}
	if t.Enabled == (strings.TrimSpace(t.DisabledReason) != "") {
		return fmt.Errorf("%s: disabled_reason must be present exactly when disabled", t.ID)
	}
	if strings.TrimSpace(t.Baseline.Reason) == "" {
		return fmt.Errorf("%s: baseline.reason is required", t.ID)
	}
	if t.Baseline.Included && !t.Enabled {
		return fmt.Errorf("%s: disabled task cannot be included in baseline", t.ID)
	}
	if t.TimeoutSeconds < 1 || t.TimeoutSeconds > 600 {
		return fmt.Errorf("%s: timeout_seconds must be in [1,600]", t.ID)
	}
	if len(t.Fixture.Files) == 0 || strings.TrimSpace(t.Fixture.Command) == "" {
		return fmt.Errorf("%s: fixture files and command are required", t.ID)
	}
	for name := range t.Fixture.Files {
		if err := validateRelativePath(name); err != nil {
			return fmt.Errorf("%s: fixture file %q: %w", t.ID, name, err)
		}
	}
	if len(t.Verify) == 0 {
		return fmt.Errorf("%s: at least one verify command is required", t.ID)
	}
	for i, verify := range t.Verify {
		if strings.TrimSpace(verify.Name) == "" || strings.TrimSpace(verify.Command) == "" {
			return fmt.Errorf("%s: verify[%d] name and command are required", t.ID, i)
		}
	}
	if t.Scoring.MaxChangedFiles < 1 || t.Scoring.MaxAddedLines < 0 || t.Scoring.MaxDeletedLines < 0 {
		return fmt.Errorf("%s: invalid scoring limits", t.ID)
	}
	for _, pattern := range t.Scoring.ForbiddenPaths {
		if err := validateRelativePath(strings.TrimSuffix(pattern, "/**")); err != nil {
			return fmt.Errorf("%s: forbidden path %q: %w", t.ID, pattern, err)
		}
	}
	return nil
}

func validateRelativePath(name string) error {
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(name)))
	if clean == "" || clean == "." || strings.HasPrefix(clean, "../") || filepath.IsAbs(name) {
		return fmt.Errorf("must be a safe relative path")
	}
	return nil
}
