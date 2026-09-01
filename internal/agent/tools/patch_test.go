package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestApplyContextPatchMultipleHunks(t *testing.T) {
	source := []byte("package demo\n\nfunc add(a, b int) int {\n\treturn a - b\n}\n\nconst timeout = 30\n")
	hunks, err := parseContextPatch(`@@ add
 func add(a, b int) int {
-	return a - b
+	return a + b
 }
@@ timeout
-const timeout = 30
+const timeout = 60`)
	if err != nil {
		t.Fatalf("parseContextPatch: %v", err)
	}
	got, applied, err := applyContextPatch(context.Background(), source, hunks)
	if err != nil {
		t.Fatalf("applyContextPatch: %v", err)
	}
	want := "package demo\n\nfunc add(a, b int) int {\n\treturn a + b\n}\n\nconst timeout = 60\n"
	if string(got) != want {
		t.Fatalf("patched content:\n%s\nwant:\n%s", got, want)
	}
	if len(applied) != 2 {
		t.Fatalf("applied hunks = %d, want 2", len(applied))
	}
}

func TestApplyContextPatchRejectsAmbiguousContext(t *testing.T) {
	source := []byte("value := 1\nvalue := 1\n")
	hunks, err := parseContextPatch("@@\n-value := 1\n+value := 2")
	if err != nil {
		t.Fatalf("parseContextPatch: %v", err)
	}
	_, _, err = applyContextPatch(context.Background(), source, hunks)
	if err == nil || !strings.Contains(err.Error(), "matches 2 locations") {
		t.Fatalf("error = %v, want ambiguous-context error", err)
	}
}

func TestApplyContextPatchFailureLeavesEarlierResultUncommitted(t *testing.T) {
	source := []byte("first := 1\nsecond := 2\n")
	hunks, err := parseContextPatch(`@@
-first := 1
+first := 10
@@
-missing := 3
+missing := 30`)
	if err != nil {
		t.Fatalf("parseContextPatch: %v", err)
	}
	got, _, err := applyContextPatch(context.Background(), source, hunks)
	if err == nil {
		t.Fatal("expected second hunk to fail")
	}
	if got != nil {
		t.Fatalf("failed patch returned partial content: %q", got)
	}
	if string(source) != "first := 1\nsecond := 2\n" {
		t.Fatalf("source mutated: %q", source)
	}
}

func TestApplyContextPatchPreservesCRLF(t *testing.T) {
	source := []byte("first\r\nsecond\r\n")
	hunks, err := parseContextPatch("@@\n-first\n+changed")
	if err != nil {
		t.Fatalf("parseContextPatch: %v", err)
	}
	got, _, err := applyContextPatch(context.Background(), source, hunks)
	if err != nil {
		t.Fatalf("applyContextPatch: %v", err)
	}
	if string(got) != "changed\r\nsecond\r\n" {
		t.Fatalf("CRLF result = %q", got)
	}
}

func TestParseContextPatchRequiresPrefixedLinesAndChanges(t *testing.T) {
	tests := []string{
		"plain text",
		"@@\n context only",
		"@@\n\n-old\n+new",
		"@@\n+insertion without context",
	}
	for _, patch := range tests {
		t.Run(strings.ReplaceAll(patch, "\n", "_"), func(t *testing.T) {
			if _, err := parseContextPatch(patch); err == nil {
				t.Fatalf("parseContextPatch(%q) unexpectedly succeeded", patch)
			}
		})
	}
}

func TestApplyContextPatchHonorsCancellation(t *testing.T) {
	hunks, err := parseContextPatch("@@\n-old\n+new")
	if err != nil {
		t.Fatalf("parseContextPatch: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = applyContextPatch(ctx, []byte("old\n"), hunks)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestAtomicReplaceIfUnchangedPreservesMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "script.sh")
	original := []byte("#!/bin/sh\necho old\n")
	next := []byte("#!/bin/sh\necho new\n")
	if err := os.WriteFile(path, original, 0o750); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := atomicReplaceIfUnchanged(context.Background(), path, original, next, 0o750); err != nil {
		t.Fatalf("atomicReplaceIfUnchanged: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != string(next) {
		t.Fatalf("content = %q, want %q", got, next)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat result: %v", err)
	}
	if info.Mode().Perm() != 0o750 {
		t.Fatalf("mode = %o, want 750", info.Mode().Perm())
	}
}

func TestAtomicReplaceIfUnchangedRejectsConflict(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("current\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	err := atomicReplaceIfUnchanged(
		context.Background(),
		path,
		[]byte("stale\n"),
		[]byte("next\n"),
		0o644,
	)
	if err == nil || !strings.Contains(err.Error(), "target changed") {
		t.Fatalf("error = %v, want conflict", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read result: %v", readErr)
	}
	if string(got) != "current\n" {
		t.Fatalf("conflict overwrote target: %q", got)
	}
}

func TestApplyPatchFileWritesInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n\nconst value = 1\n"), 0o640); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	input := &ApplyPatchInput{
		Path:  "main.go",
		Patch: "@@\n-const value = 1\n+const value = 2",
	}
	hunks, err := parseContextPatch(input.Patch)
	if err != nil {
		t.Fatalf("parseContextPatch: %v", err)
	}
	out, err := applyPatchFile(context.Background(), root, input, hunks)
	if err != nil {
		t.Fatalf("applyPatchFile: %v", err)
	}
	if out.Path != "main.go" || out.Hunks != 1 || out.Additions != 1 || out.Deletions != 1 {
		t.Fatalf("output = %#v", out)
	}
	if len(out.HunkDetails) != 1 || out.HunkDetails[0].Line != 3 {
		t.Fatalf("hunk details = %#v, want line 3", out.HunkDetails)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "package main\n\nconst value = 2\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestApplyPatchFileRejectsBinaryAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	binaryPath := filepath.Join(root, "binary.dat")
	if err := os.WriteFile(binaryPath, []byte("old\x00value"), 0o644); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	binaryInput := &ApplyPatchInput{Path: "binary.dat", Patch: "@@\n-old\n+new"}
	binaryHunks, err := parseContextPatch(binaryInput.Patch)
	if err != nil {
		t.Fatalf("parse binary patch: %v", err)
	}
	if _, err := applyPatchFile(context.Background(), root, binaryInput, binaryHunks); err == nil {
		t.Fatal("binary patch unexpectedly succeeded")
	}

	outside := t.TempDir()
	outsidePath := filepath.Join(outside, "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(outsidePath, filepath.Join(root, "escape.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	symlinkInput := &ApplyPatchInput{Path: "escape.txt", Patch: "@@\n-old\n+new"}
	symlinkHunks, err := parseContextPatch(symlinkInput.Patch)
	if err != nil {
		t.Fatalf("parse symlink patch: %v", err)
	}
	if _, err := applyPatchFile(context.Background(), root, symlinkInput, symlinkHunks); err == nil {
		t.Fatal("symlink escape patch unexpectedly succeeded")
	}
	got, err := os.ReadFile(outsidePath)
	if err != nil {
		t.Fatalf("read outside: %v", err)
	}
	if string(got) != "old\n" {
		t.Fatalf("outside file changed: %q", got)
	}
}

func TestApplyContextPatchFuzzyWhitespaceTolerance(t *testing.T) {
	// The removed line carries trailing whitespace in the file, which breaks an
	// exact match; the hunk's distinctive `func add` context line anchors the
	// fuzzy fallback so the edit still lands in the right block.
	source := []byte("package demo\n\nfunc add(a, b int) int {\n\treturn a - b   \n}\n\nfunc sub(a, b int) int {\n\treturn a - b\n}\n")
	hunks, err := parseContextPatch("@@\n func add(a, b int) int {\n-\treturn a - b\n+\treturn a + b\n }")
	if err != nil {
		t.Fatalf("parseContextPatch: %v", err)
	}
	got, applied, err := applyContextPatch(context.Background(), source, hunks)
	if err != nil {
		t.Fatalf("applyContextPatch fuzzy: %v", err)
	}
	want := "package demo\n\nfunc add(a, b int) int {\n\treturn a + b\n}\n\nfunc sub(a, b int) int {\n\treturn a - b\n}\n"
	if string(got) != want {
		t.Fatalf("patched content:\n%s\nwant:\n%s", got, want)
	}
	if len(applied) != 1 || !applied[0].fuzzy {
		t.Fatalf("applied=%#v, want exactly one hunk marked fuzzy", applied)
	}
}

func TestApplyContextPatchFuzzyRejectsAmbiguous(t *testing.T) {
	// Two equally-plausible trailing-whitespace windows leave no unambiguous
	// anchor, so the fuzzy fallback must reject rather than guess.
	source := []byte("a \nb\na \nb\n")
	hunks, err := parseContextPatch("@@\n a\n-b\n+c")
	if err != nil {
		t.Fatalf("parseContextPatch: %v", err)
	}
	_, _, err = applyContextPatch(context.Background(), source, hunks)
	if err == nil || !strings.Contains(err.Error(), "context was not found") {
		t.Fatalf("error = %v, want not-found after ambiguous fuzzy", err)
	}
}

func TestObservedStateMatchesAndConflicts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("package a\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Reading yields a state; writing it back matches (no-op) / no conflict.
	state := observedState(path)
	if state == "" {
		t.Fatal("observedState returned empty for an existing file")
	}
	if err := verifyObservedState(path, "a.go", "write", ""); err != nil {
		t.Fatalf("empty observed_state should be a no-op, got %v", err)
	}
	if err := verifyObservedState(path, "a.go", "write", state); err != nil {
		t.Fatalf("matching observed_state should pass, got %v", err)
	}

	// Changing the file makes the old state a conflict.
	time.Sleep(10 * time.Millisecond) // ensure mtime moves
	if err := os.WriteFile(path, []byte("package a\n\nfunc A() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := verifyObservedState(path, "a.go", "patch", state)
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("stale observed_state should conflict, got %v", err)
	}

	// A deleted file is a conflict for an observed write, and empty for a fresh one.
	gone := filepath.Join(dir, "gone.go")
	if err := os.WriteFile(gone, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gState := observedState(gone)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if err := verifyObservedState(gone, "gone.go", "write", gState); err == nil {
		t.Fatal("observed write to a deleted file should conflict")
	}
	if err := verifyObservedState(gone, "gone.go", "write", ""); err != nil {
		t.Fatalf("observed_state empty to a deleted file should pass, got %v", err)
	}
}

func TestApplyPatchRejectsStaleObservedState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")

	if err := os.WriteFile(path, []byte("package a\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := observedState(path)
	time.Sleep(10 * time.Millisecond) // ensure mtime moves
	if err := os.WriteFile(path, []byte("package a\n\nfunc A() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	hunks, err := parseContextPatch("@@\n-func A() {}\n+func A() int { return 1 }")
	if err != nil {
		t.Fatal(err)
	}
	// The file changed since the model read it → the whole patch is a conflict,
	// detected before any context matching.
	in := &ApplyPatchInput{Path: "a.go", Patch: "@@\n-func A() {}\n+func A() int { return 1 }", ObservedState: state}
	if _, err := applyPatchFile(context.Background(), dir, in, hunks); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("stale observed_state should conflict, got %v", err)
	}

	// Restore content the hunk matches; a fresh observed_state lets it apply.
	if err := os.WriteFile(path, []byte("package a\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fresh := observedState(path)
	in2 := &ApplyPatchInput{Path: "a.go", Patch: in.Patch, ObservedState: fresh}
	if _, err := applyPatchFile(context.Background(), dir, in2, hunks); err != nil {
		t.Fatalf("fresh observed_state should pass, got %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "package a\n\nfunc A() int { return 1 }\n" {
		t.Fatalf("patched content = %q", got)
	}
}
