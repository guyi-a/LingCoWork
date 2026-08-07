package instructions

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	MaxNameRunes        = 64
	MaxLabelRunes       = 100
	MaxDescriptionRunes = 500
	MaxPromptBytes      = 32 << 10
	MaxFileBytes        = 64 << 10
)

var (
	ErrNotFound      = errors.New("instruction not found")
	ErrAlreadyExists = errors.New("instruction already exists")
	ErrInvalid       = errors.New("invalid instruction")

	namePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
)

type Instruction struct {
	Name        string `json:"name" yaml:"name"`
	Label       string `json:"label" yaml:"label"`
	Description string `json:"description" yaml:"description"`
	Prompt      string `json:"prompt" yaml:"-"`
}

type UserInstruction struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	RawInput string `json:"raw_input"`
}

type MessageExtra struct {
	UserInstruction *UserInstruction `json:"user_instruction,omitempty"`
}

type Store struct {
	root string
	mu   sync.RWMutex
}

func NewStore(root string) *Store {
	return &Store{root: filepath.Clean(root)}
}

func (s *Store) Root() string {
	return s.root
}

func Validate(in Instruction) error {
	if err := ValidateName(in.Name); err != nil {
		return err
	}
	if strings.TrimSpace(in.Label) == "" {
		return invalidf("label is required")
	}
	if utf8.RuneCountInString(in.Label) > MaxLabelRunes {
		return invalidf("label exceeds %d characters", MaxLabelRunes)
	}
	if strings.TrimSpace(in.Description) == "" {
		return invalidf("description is required")
	}
	if utf8.RuneCountInString(in.Description) > MaxDescriptionRunes {
		return invalidf("description exceeds %d characters", MaxDescriptionRunes)
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return invalidf("prompt is required")
	}
	if len(in.Prompt) > MaxPromptBytes {
		return invalidf("prompt exceeds %d bytes", MaxPromptBytes)
	}
	return nil
}

func ValidateName(name string) error {
	if name == "" {
		return invalidf("name is required")
	}
	if utf8.RuneCountInString(name) > MaxNameRunes || !namePattern.MatchString(name) {
		return invalidf("name must be 1-%d lowercase letters, digits, or hyphens", MaxNameRunes)
	}
	return nil
}

func (s *Store) List() ([]Instruction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.root)
	if errors.Is(err, fs.ErrNotExist) {
		return []Instruction{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read instructions directory: %w", err)
	}
	out := make([]Instruction, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".md")
		if ValidateName(name) != nil {
			continue
		}
		in, err := s.readLocked(name)
		if err != nil {
			return nil, fmt.Errorf("read instruction %q: %w", name, err)
		}
		out = append(out, *in)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *Store) Get(name string) (*Instruction, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readLocked(name)
}

func (s *Store) Create(in Instruction) error {
	if err := Validate(in); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := os.Lstat(s.path(in.Name)); err == nil {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, in.Name)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("check instruction %q: %w", in.Name, err)
	}
	return s.writeLocked(in)
}

func (s *Store) Update(name string, in Instruction) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if in.Name != name {
		return invalidf("frontmatter name must match path name")
	}
	if err := Validate(in); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := os.Lstat(s.path(name)); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	} else if err != nil {
		return fmt.Errorf("check instruction %q: %w", name, err)
	}
	return s.writeLocked(in)
}

func (s *Store) Delete(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.Remove(s.path(name)); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	} else if err != nil {
		return fmt.Errorf("delete instruction %q: %w", name, err)
	}
	return nil
}

func Parse(data []byte) (*Instruction, error) {
	if len(data) > MaxFileBytes {
		return nil, invalidf("file exceeds %d bytes", MaxFileBytes)
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if !bytes.HasPrefix(data, []byte("---\n")) {
		return nil, invalidf("YAML frontmatter is required")
	}
	rest := data[len("---\n"):]
	end := bytes.Index(rest, []byte("\n---"))
	if end < 0 {
		return nil, invalidf("YAML frontmatter is not closed")
	}
	after := rest[end+len("\n---"):]
	if len(after) > 0 && after[0] == '\r' {
		after = after[1:]
	}
	if len(after) > 0 && after[0] != '\n' {
		return nil, invalidf("invalid frontmatter closing delimiter")
	}
	after = bytes.TrimPrefix(after, []byte("\n"))

	var meta struct {
		Name        string `yaml:"name"`
		Label       string `yaml:"label"`
		Description string `yaml:"description"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(rest[:end]))
	decoder.KnownFields(true)
	if err := decoder.Decode(&meta); err != nil {
		return nil, invalidf("parse YAML frontmatter: %v", err)
	}
	in := &Instruction{
		Name:        meta.Name,
		Label:       meta.Label,
		Description: meta.Description,
		Prompt:      string(after),
	}
	if err := Validate(*in); err != nil {
		return nil, err
	}
	return in, nil
}

func Marshal(in Instruction) ([]byte, error) {
	if err := Validate(in); err != nil {
		return nil, err
	}
	meta := struct {
		Name        string `yaml:"name"`
		Label       string `yaml:"label"`
		Description string `yaml:"description"`
	}{
		Name:        in.Name,
		Label:       in.Label,
		Description: in.Description,
	}
	frontmatter, err := yaml.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("marshal YAML frontmatter: %w", err)
	}
	out := append([]byte("---\n"), frontmatter...)
	out = append(out, []byte("---\n")...)
	out = append(out, []byte(in.Prompt)...)
	if len(out) > MaxFileBytes {
		return nil, invalidf("file exceeds %d bytes", MaxFileBytes)
	}
	return out, nil
}

func Expand(prompt, input string) string {
	if strings.Contains(prompt, "{{input}}") {
		// Attachment markers are line-oriented. When a template places
		// {{input}} inline ("Review: {{input}}"), a leading marker would stop
		// matching multimodal.BuildUserMessage's full-line grammar and the
		// attachment would silently disappear. Put marker-led input on a fresh
		// line while preserving ordinary text interpolation exactly.
		if strings.HasPrefix(strings.TrimLeft(input, "\r\n"), "[image:") {
			input = "\n" + input
		}
		return strings.ReplaceAll(prompt, "{{input}}", input)
	}
	if input == "" {
		return prompt
	}
	return prompt + "\n\n" + input
}

func (s *Store) path(name string) string {
	return filepath.Join(s.root, name+".md")
}

func (s *Store) readLocked(name string) (*Instruction, error) {
	path := s.path(name)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, invalidf("instruction path must be a regular Markdown file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	in, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if in.Name != name {
		return nil, invalidf("frontmatter name %q does not match filename %q", in.Name, name)
	}
	return in, nil
}

func (s *Store) writeLocked(in Instruction) error {
	data, err := Marshal(in)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return fmt.Errorf("create instructions directory: %w", err)
	}
	tmp, err := os.CreateTemp(s.root, "."+in.Name+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary instruction: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("set temporary instruction permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary instruction: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temporary instruction: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary instruction: %w", err)
	}
	if err := os.Rename(tmpName, s.path(in.Name)); err != nil {
		return fmt.Errorf("replace instruction: %w", err)
	}
	return nil
}

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}
