package validation

import (
	"strconv"
	"strings"
	"testing"
)

func TestParseTypeScriptAndRejectExternalPath(t *testing.T) {
	summary := Parse(
		KindTypecheck,
		"tsc --pretty false",
		"/repo",
		"/repo",
		"src/app.ts(12,7): error TS2322: Type string is not assignable\n"+
			"/tmp/external.ts(1,2): error TS1000: outside\n",
		"",
		1,
	)
	if !summary.ParseOK || summary.ErrorCount != 2 {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.Diagnostics[0].Path != "src/app.ts" ||
		summary.Diagnostics[0].Line != 12 ||
		summary.Diagnostics[0].Code != "TS2322" {
		t.Fatalf("first diagnostic = %#v", summary.Diagnostics[0])
	}
	if summary.Diagnostics[1].Path != "" {
		t.Fatalf("external path became clickable: %#v", summary.Diagnostics[1])
	}
}

func TestParseESLintJSONAndStylish(t *testing.T) {
	jsonSummary := Parse(
		KindLint, "eslint -f json", "/repo", "/repo",
		`[{"filePath":"/repo/web/a.ts","messages":[{"ruleId":"no-x","severity":2,"message":"bad","line":3,"column":4}]}]`,
		"", 1,
	)
	if jsonSummary.Parser != "eslint-json" ||
		jsonSummary.Diagnostics[0].Path != "web/a.ts" {
		t.Fatalf("eslint json = %#v", jsonSummary)
	}
	stylish := Parse(
		KindLint, "eslint .", "/repo", "/repo",
		"", "/repo/web/a.ts\n  3:4  warning  avoid x  no-x\n", 1,
	)
	if stylish.Parser != "eslint-stylish" ||
		stylish.WarningCount != 1 {
		t.Fatalf("eslint stylish = %#v", stylish)
	}
}

func TestParseGoTestJSONAndDeduplicate(t *testing.T) {
	raw := strings.Join([]string{
		`{"Action":"output","Package":"p","Test":"TestX","Output":"foo_test.go:9: expected 1, got 2\n"}`,
		`{"Action":"output","Package":"p","Test":"TestX","Output":"foo_test.go:9: expected 1, got 2\n"}`,
		`{"Action":"fail","Package":"p","Test":"TestX"}`,
	}, "\n")
	summary := Parse(
		KindTest, "go test -json ./...", "/repo", "/repo", raw, "", 1,
	)
	if summary.Parser != "go-test-json" || !summary.ParseOK {
		t.Fatalf("go summary = %#v", summary)
	}
	if len(summary.Diagnostics) != 2 {
		t.Fatalf("diagnostics = %#v", summary.Diagnostics)
	}
}

func TestFailedUnparsedCommandGetsCommandProblem(t *testing.T) {
	summary := Parse(
		KindBuild, "make build", "/repo", "/repo", "", "build exploded", 2,
	)
	if summary.ParseOK || len(summary.Diagnostics) != 1 ||
		summary.Diagnostics[0].Path != "" {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestDiagnosticCap(t *testing.T) {
	var lines []string
	for i := 1; i <= MaxDiagnostics+20; i++ {
		lines = append(lines, "file.go:"+strconv.Itoa(i)+": error: failure "+strconv.Itoa(i))
	}
	summary := Parse(
		KindBuild, "go build ./...", "/repo", "/repo",
		strings.Join(lines, "\n"), "", 1,
	)
	if len(summary.Diagnostics) != MaxDiagnostics || !summary.Truncated {
		t.Fatalf("count=%d truncated=%v", len(summary.Diagnostics), summary.Truncated)
	}
}
