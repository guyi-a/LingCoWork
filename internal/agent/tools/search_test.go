package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGlobSearchRespectsIgnoreAndSafetyBoundaries(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "main.go", "package main\n")
	writeSearchFixture(t, root, "src/handler.go", "package src\n")
	writeSearchFixture(t, root, "src/ignored.go", "package src\n")
	writeSearchFixture(t, root, "src/.gitignore", "ignored.go\n")
	writeSearchFixture(t, root, "node_modules/dependency.go", "package dependency\n")
	writeSearchFixture(t, root, ".env.production", "TOKEN=secret\n")

	outside := t.TempDir()
	writeSearchFixture(t, outside, "outside.go", "package outside\n")
	if err := os.Symlink(filepath.Join(outside, "outside.go"), filepath.Join(root, "linked.go")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	out, err := globSearch(context.Background(), root, root, ".", &GlobInput{
		Pattern: "**/*.go",
	})
	if err != nil {
		t.Fatalf("globSearch: %v", err)
	}
	want := []string{"main.go", "src/handler.go"}
	if !reflect.DeepEqual(out.Matches, want) {
		t.Fatalf("matches = %#v, want %#v", out.Matches, want)
	}
	if out.Truncated {
		t.Fatalf("unexpected truncation: %s", out.Reason)
	}
}

func TestGlobSearchSupportsSubdirectoryRoot(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, ".gitignore", "*.generated.ts\n")
	writeSearchFixture(t, root, "web/app.ts", "export {}\n")
	writeSearchFixture(t, root, "web/app.generated.ts", "export {}\n")
	start := filepath.Join(root, "web")

	out, err := globSearch(context.Background(), root, start, "web", &GlobInput{
		Pattern: "*.ts",
	})
	if err != nil {
		t.Fatalf("globSearch: %v", err)
	}
	if want := []string{"web/app.ts"}; !reflect.DeepEqual(out.Matches, want) {
		t.Fatalf("matches = %#v, want %#v", out.Matches, want)
	}
}

func TestGrepSearchReturnsStructuredMatchesAndContext(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "a.go", "package demo\n\nfunc Hello() {\n\tprintln(\"needle\")\n}\n")
	writeSearchFixture(t, root, "b.ts", "const NEEDLE = true\n")
	writeSearchFixture(t, root, ".env", "needle=secret\n")
	writeSearchFixture(t, root, "binary.go", "needle\x00hidden")

	out, err := grepSearch(context.Background(), root, root, ".", &GrepInput{
		Pattern:         "needle",
		CaseInsensitive: true,
		Glob:            "**/*.{go,ts}",
		ContextLines:    1,
	})
	if err != nil {
		t.Fatalf("grepSearch: %v", err)
	}
	if out.MatchCount != 2 {
		t.Fatalf("match_count = %d, want 2: %#v", out.MatchCount, out.Matches)
	}
	first := out.Matches[0]
	if first.Path != "a.go" || first.Line != 4 || first.Column != 11 {
		t.Fatalf("first match = %#v", first)
	}
	if !reflect.DeepEqual(first.Before, []string{"func Hello() {"}) ||
		!reflect.DeepEqual(first.After, []string{"}"}) {
		t.Fatalf("context = before %#v after %#v", first.Before, first.After)
	}
	if out.FilesSkipped != 1 {
		t.Fatalf("files_skipped = %d, want 1 binary file", out.FilesSkipped)
	}
}

func TestGrepSearchMarksResultLimit(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "many.txt", "hit one\nhit two\n")

	out, err := grepSearch(context.Background(), root, root, ".", &GrepInput{
		Pattern:    "hit",
		MaxMatches: 1,
	})
	if err != nil {
		t.Fatalf("grepSearch: %v", err)
	}
	if out.MatchCount != 1 || !out.Truncated || out.Reason != "maximum matches reached" {
		t.Fatalf("unexpected bounded result: %#v", out)
	}
}

func TestSearchHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "file.txt", "content\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := globSearch(ctx, root, root, ".", &GlobInput{Pattern: "**/*"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("globSearch error = %v, want context.Canceled", err)
	}
}

func TestGrepSearchRejectsInvalidRegex(t *testing.T) {
	root := t.TempDir()
	_, err := grepSearch(context.Background(), root, root, ".", &GrepInput{Pattern: "["})
	if err == nil {
		t.Fatal("expected invalid regex error")
	}
}

func TestIgnoredSearchPathBlocksExplicitDependencyAndCredentialRoots(t *testing.T) {
	for _, path := range []string{
		"node_modules/pkg/index.js",
		"src/vendor/library.go",
		".lingcowork/mcp.json",
	} {
		if !ignoredSearchPath(path) {
			t.Errorf("ignoredSearchPath(%q) = false", path)
		}
	}
	if ignoredSearchPath("src/config/env_setup.go") {
		t.Error("ordinary source path was excluded")
	}
	if ignoredSearchPath("config/.env.local") {
		t.Error("dotenv path should follow ordinary workspace ignore rules")
	}
}

func writeSearchFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}
