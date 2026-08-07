package service

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/guyi-a/Interview-Agent/internal/instructions"
)

func TestPrepareUserMessageLoadsAuthoritativeInstruction(t *testing.T) {
	store := instructions.NewStore(filepath.Join(t.TempDir(), "instructions"))
	if err := store.Create(instructions.Instruction{
		Name:        "review",
		Label:       "Code review",
		Description: "Review code carefully.",
		Prompt:      "Review carefully:\n\n{{input}}",
	}); err != nil {
		t.Fatal(err)
	}
	svc := &ChatService{instructions: store}

	got, err := svc.prepareUserMessage("package main", "review")
	if err != nil {
		t.Fatalf("prepareUserMessage: %v", err)
	}
	if got.content != "Review carefully:\n\npackage main" {
		t.Fatalf("content=%q", got.content)
	}
	if got.titleSource != "package main" {
		t.Fatalf("titleSource=%q", got.titleSource)
	}
	var extra instructions.MessageExtra
	if err := json.Unmarshal([]byte(got.extra), &extra); err != nil {
		t.Fatalf("unmarshal extra: %v", err)
	}
	if extra.UserInstruction == nil ||
		extra.UserInstruction.Name != "review" ||
		extra.UserInstruction.Label != "Code review" ||
		extra.UserInstruction.RawInput != "package main" {
		t.Fatalf("extra=%+v", extra)
	}
}

func TestPrepareUserMessageInstructionOnlyUsesLabelForTitle(t *testing.T) {
	store := instructions.NewStore(filepath.Join(t.TempDir(), "instructions"))
	if err := store.Create(instructions.Instruction{
		Name:        "brainstorm",
		Label:       "Brainstorm",
		Description: "Generate ideas.",
		Prompt:      "Generate five ideas.",
	}); err != nil {
		t.Fatal(err)
	}
	svc := &ChatService{instructions: store}

	got, err := svc.prepareUserMessage("", "brainstorm")
	if err != nil {
		t.Fatal(err)
	}
	if got.content != "Generate five ideas." || got.titleSource != "Brainstorm" {
		t.Fatalf("prepared=%+v", got)
	}
}

func TestPrepareUserMessageMissingInstruction(t *testing.T) {
	svc := &ChatService{instructions: instructions.NewStore(filepath.Join(t.TempDir(), "instructions"))}
	if _, err := svc.prepareUserMessage("hello", "missing"); !errors.Is(err, instructions.ErrNotFound) {
		t.Fatalf("error=%v", err)
	}
}

func TestPrepareUserMessageWithoutInstructionPreservesMessage(t *testing.T) {
	svc := &ChatService{}
	got, err := svc.prepareUserMessage("[image: /tmp/a.png]\nhello", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.content != "[image: /tmp/a.png]\nhello" || got.titleSource != got.content || got.extra != "" {
		t.Fatalf("prepared=%+v", got)
	}
}
