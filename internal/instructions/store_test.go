package instructions

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleInstruction() Instruction {
	return Instruction{
		Name:        "code-review",
		Label:       "Code review",
		Description: "Review code for correctness.",
		Prompt:      "Review this:\n\n{{input}}",
	}
}

func TestParseMarkdown(t *testing.T) {
	raw := []byte("---\nname: code-review\nlabel: Code review\ndescription: Review code.\n---\nReview:\n{{input}}\n")
	got, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Name != "code-review" || got.Label != "Code review" || got.Prompt != "Review:\n{{input}}\n" {
		t.Fatalf("unexpected instruction: %+v", got)
	}
}

func TestParseRejectsUnknownFrontmatterAndOversizedPrompt(t *testing.T) {
	if _, err := Parse([]byte("---\nname: demo\nlabel: Demo\ndescription: x\nscript: run.sh\n---\nprompt")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown frontmatter key error = %v", err)
	}
	in := sampleInstruction()
	in.Prompt = strings.Repeat("x", MaxPromptBytes+1)
	if err := Validate(in); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized prompt error = %v", err)
	}
}

func TestStoreCRUDAndAtomicCleanup(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".lingcowork", "instructions")
	store := NewStore(root)
	in := sampleInstruction()

	if err := store.Create(in); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Create(in); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate Create error = %v", err)
	}
	got, err := store.Get(in.Name)
	if err != nil || *got != in {
		t.Fatalf("Get = %+v, %v", got, err)
	}

	in.Label = "Updated review"
	if err := store.Update(in.Name, in); err != nil {
		t.Fatalf("Update: %v", err)
	}
	list, err := store.List()
	if err != nil || len(list) != 1 || list[0].Label != in.Label {
		t.Fatalf("List = %+v, %v", list, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("temporary file left behind: %s", entry.Name())
		}
	}

	if err := store.Delete(in.Name); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(in.Name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete error = %v", err)
	}
}

func TestStoreRejectsPathTraversalAndMismatchedUpdate(t *testing.T) {
	base := t.TempDir()
	store := NewStore(filepath.Join(base, "instructions"))
	for _, name := range []string{"../escape", "a/b", "/abs", "Bad_Name", "two--"} {
		if err := ValidateName(name); !errors.Is(err, ErrInvalid) {
			t.Errorf("ValidateName(%q) = %v", name, err)
		}
	}

	in := sampleInstruction()
	if err := store.Create(in); err != nil {
		t.Fatal(err)
	}
	in.Name = "renamed"
	if err := store.Update("code-review", in); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched update error = %v", err)
	}

	target := filepath.Join(base, "outside.md")
	if err := os.WriteFile(target, []byte("---\nname: linked\nlabel: Linked\ndescription: Linked file\n---\nprompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(store.Root(), "linked.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("linked"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("symlink Get error = %v", err)
	}
}

func TestExpand(t *testing.T) {
	tests := []struct {
		name, prompt, input, want string
	}{
		{"placeholder", "Before {{input}} after", "text", "Before text after"},
		{"all placeholders", "{{input}} / {{input}}", "x", "x / x"},
		{"append", "Summarize.", "text", "Summarize.\n\ntext"},
		{"instruction only", "Summarize.", "", "Summarize."},
		{"empty placeholder", "Do: {{input}}", "", "Do: "},
		{
			"inline placeholder preserves image marker line",
			"Review: {{input}}",
			"[image: /tmp/example.png]\nquestion",
			"Review: \n[image: /tmp/example.png]\nquestion",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Expand(tt.prompt, tt.input); got != tt.want {
				t.Fatalf("Expand() = %q, want %q", got, tt.want)
			}
		})
	}
}
