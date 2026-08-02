package mcp

import (
	"strings"
	"testing"
)

func TestNameForDoesNotShadowBuiltins(t *testing.T) {
	a := newAllocator([]string{"read_file", "run_command"})
	slug := a.slugFor("filesystem")

	got := a.nameFor(slug, "read_file")
	if got == "read_file" {
		t.Fatalf("remote tool took over the builtin name")
	}
	if want := "filesystem__read_file"; got != want {
		t.Fatalf("nameFor = %q, want %q", got, want)
	}
}

// A server named exactly like a builtin is the case where the prefix alone is
// not enough — the prefixed name has to lose the collision too.
func TestNameForCollidesWithReservedPrefixedName(t *testing.T) {
	a := newAllocator([]string{"fs__read"})
	slug := a.slugFor("fs")

	got := a.nameFor(slug, "read")
	if got == "fs__read" {
		t.Fatalf("nameFor returned the reserved name")
	}
	if want := "fs__read_2"; got != want {
		t.Fatalf("nameFor = %q, want %q", got, want)
	}
}

func TestSlugForDisambiguatesPunctuationOnlyDifferences(t *testing.T) {
	a := newAllocator(nil)
	first := a.slugFor("foo-bar")
	second := a.slugFor("foo bar")
	third := a.slugFor("foo_bar")

	if first == second || second == third || first == third {
		t.Fatalf("slugs collided: %q %q %q", first, second, third)
	}
}

func TestSlugForNonASCIIServerName(t *testing.T) {
	a := newAllocator(nil)
	got := a.slugFor("文件系统")
	if got != fallbackSlug {
		t.Fatalf("slugFor = %q, want %q", got, fallbackSlug)
	}
	// A second all-Chinese name must not reuse it.
	if next := a.slugFor("数据库"); next == got {
		t.Fatalf("two non-ASCII server names got the same slug %q", next)
	}
}

func TestNameForCapsAtProviderLimit(t *testing.T) {
	a := newAllocator(nil)
	slug := a.slugFor(strings.Repeat("server", 8))
	got := a.nameFor(slug, strings.Repeat("tool", 12))

	if len(got) > maxToolNameLen {
		t.Fatalf("name is %d chars, over the %d limit: %q", len(got), maxToolNameLen, got)
	}
}

// The public name ends up in stored transcripts, so the same tool must get the
// same name on the next boot even when it had to be truncated.
func TestNameForIsStableAcrossRuns(t *testing.T) {
	long := strings.Repeat("tool", 12)
	name := func() string {
		a := newAllocator(nil)
		return a.nameFor(a.slugFor(strings.Repeat("server", 8)), long)
	}
	if first, second := name(), name(); first != second {
		t.Fatalf("name not stable: %q then %q", first, second)
	}
}

// Truncation must not merge two distinct tools onto one name.
func TestNameForTruncationStaysUnique(t *testing.T) {
	a := newAllocator(nil)
	slug := a.slugFor("s")
	prefix := strings.Repeat("x", 70)

	first := a.nameFor(slug, prefix+"alpha")
	second := a.nameFor(slug, prefix+"beta")
	if first == second {
		t.Fatalf("two tools truncated to the same name %q", first)
	}
}

func TestSanitize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Read File", "read_file"},
		{"read-file", "read-file"},
		{"__read__", "read"},
		{"a _ b", "a_b"},
		{"文件", ""},
		{"tool@v2", "tool_v2"},
		{"  spaced  ", "spaced"},
		{"UPPER_CASE", "upper_case"},
	}
	for _, c := range cases {
		if got := sanitize(c.in); got != c.want {
			t.Errorf("sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
