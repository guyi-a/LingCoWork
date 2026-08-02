package service

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateForTitle(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"short message is kept whole", "帮我看下这段代码", "帮我看下这段代码"},
		{"surrounding whitespace goes", "  改一下样式  ", "改一下样式"},
		{"empty", "   \n\t ", ""},
		{"only the opening line survives",
			"写一份技术报告\n\n要求如下：\n- 第一点\n- 第二点",
			"写一份技术报告"},
		{"carriage returns count as a line break", "第一行\r\n第二行", "第一行"},
		{"runs of whitespace collapse", "a    b\t\tc", "a b c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateForTitle(tt.in); got != tt.want {
				t.Errorf("truncateForTitle(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTruncateForTitleCutsOnRuneBoundaries(t *testing.T) {
	// Every rune here is three bytes, so a byte-based cut would leave a
	// fragment that renders as U+FFFD.
	long := strings.Repeat("调度模型", 40)
	got := truncateForTitle(long)

	if !utf8.ValidString(got) {
		t.Fatalf("title is not valid UTF-8: %q", got)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Errorf("title contains a replacement character: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a truncated title should say so, got %q", got)
	}
	if n := utf8.RuneCountInString(got); n != titleMaxRunes+1 {
		t.Errorf("got %d runes, want %d plus the ellipsis", n, titleMaxRunes)
	}
}

func TestTruncateForTitleLeavesTheLimitAlone(t *testing.T) {
	exact := strings.Repeat("字", titleMaxRunes)
	if got := truncateForTitle(exact); got != exact {
		t.Errorf("a title of exactly the limit was altered: %q", got)
	}
}
