package validation

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type Kind string

const (
	KindTest       Kind = "test"
	KindBuild      Kind = "build"
	KindLint       Kind = "lint"
	KindTypecheck  Kind = "typecheck"
	KindFormat     Kind = "format"
	MaxDiagnostics      = 200
)

type Diagnostic struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
	Code     string `json:"code,omitempty"`
	Source   string `json:"source,omitempty"`
}

type Summary struct {
	Kind         Kind         `json:"kind"`
	Passed       bool         `json:"passed"`
	Parser       string       `json:"parser"`
	ParseOK      bool         `json:"parse_ok"`
	Diagnostics  []Diagnostic `json:"diagnostics,omitempty"`
	ErrorCount   int          `json:"error_count"`
	WarningCount int          `json:"warning_count"`
	Truncated    bool         `json:"truncated,omitempty"`
}

var (
	locationPattern = regexp.MustCompile(`^(.+?):(\d+)(?::(\d+))?:\s*(?:(error|warning|info)(?:\s+([A-Za-z]+\d+))?:\s*)?(.+)$`)
	tsPattern       = regexp.MustCompile(`^(.+?)\((\d+),(\d+)\):\s*(error|warning)\s+([A-Za-z]+\d+):\s*(.+)$`)
	eslintLine      = regexp.MustCompile(`^\s*(\d+):(\d+)\s+(error|warning)\s+(.+?)(?:\s{2,}(\S+))?\s*$`)
)

func Parse(
	kind Kind,
	command, workspace, cwd, stdout, stderr string,
	exitCode int,
) Summary {
	summary := Summary{Kind: kind, Passed: exitCode == 0, Parser: "text"}
	var candidates []Diagnostic

	if diagnostics, ok := parseESLintJSON(stdout, workspace, cwd); ok {
		summary.Parser = "eslint-json"
		summary.ParseOK = true
		candidates = append(candidates, diagnostics...)
	} else if strings.Contains(command, "go test") && strings.Contains(command, "-json") {
		summary.Parser = "go-test-json"
		diagnostics, ok := parseGoTestJSON(stdout, workspace, cwd)
		summary.ParseOK = ok
		candidates = append(candidates, diagnostics...)
	} else {
		diagnostics, parser, ok := parseText(stdout+"\n"+stderr, workspace, cwd)
		summary.Parser = parser
		summary.ParseOK = ok
		candidates = append(candidates, diagnostics...)
	}
	if summary.Passed && !summary.ParseOK && len(candidates) == 0 {
		summary.Parser = "exit-code"
		summary.ParseOK = true
	}

	candidates = dedupe(candidates)
	if len(candidates) > MaxDiagnostics {
		candidates = candidates[:MaxDiagnostics]
		summary.Truncated = true
	}
	if !summary.Passed && len(candidates) == 0 {
		message := firstUsefulLine(stderr)
		if message == "" {
			message = firstUsefulLine(stdout)
		}
		if message == "" {
			message = fmt.Sprintf("%s command exited with code %d", kind, exitCode)
		}
		candidates = append(candidates, makeDiagnostic("error", message, "", 0, 0, "", string(kind)))
	}
	summary.Diagnostics = candidates
	for _, diagnostic := range candidates {
		switch diagnostic.Severity {
		case "warning":
			summary.WarningCount++
		case "info":
			// Informational diagnostics are displayed but do not make a
			// validation unhealthy.
		default:
			summary.ErrorCount++
		}
	}
	return summary
}

func ParseKind(raw string) (Kind, bool) {
	switch Kind(strings.ToLower(strings.TrimSpace(raw))) {
	case KindTest:
		return KindTest, true
	case KindBuild:
		return KindBuild, true
	case KindLint:
		return KindLint, true
	case KindTypecheck:
		return KindTypecheck, true
	case KindFormat:
		return KindFormat, true
	default:
		return "", false
	}
}

func parseGoTestJSON(raw, workspace, cwd string) ([]Diagnostic, bool) {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var out []Diagnostic
	seenEvent := false
	for scanner.Scan() {
		var event struct {
			Action  string `json:"Action"`
			Package string `json:"Package"`
			Test    string `json:"Test"`
			Output  string `json:"Output"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		seenEvent = true
		if event.Action == "fail" && event.Test != "" && event.Output == "" {
			out = append(out, makeDiagnostic(
				"error", "Test failed: "+event.Test, "", 0, 0, "", "go test",
			))
			continue
		}
		if event.Output == "" {
			continue
		}
		diagnostics, _, _ := parseText(event.Output, workspace, cwd)
		out = append(out, diagnostics...)
		if event.Action == "fail" && event.Test != "" && len(diagnostics) == 0 {
			out = append(out, makeDiagnostic(
				"error", "Test failed: "+event.Test, "", 0, 0, "", "go test",
			))
		}
	}
	return out, seenEvent
}

func parseESLintJSON(raw, workspace, cwd string) ([]Diagnostic, bool) {
	var reports []struct {
		FilePath string `json:"filePath"`
		Messages []struct {
			RuleID   string `json:"ruleId"`
			Severity int    `json:"severity"`
			Message  string `json:"message"`
			Line     int    `json:"line"`
			Column   int    `json:"column"`
		} `json:"messages"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &reports) != nil || reports == nil {
		return nil, false
	}
	var out []Diagnostic
	for _, report := range reports {
		path := normalizePath(report.FilePath, workspace, cwd)
		for _, message := range report.Messages {
			severity := "warning"
			if message.Severity >= 2 {
				severity = "error"
			}
			out = append(out, makeDiagnostic(
				severity, message.Message, path, message.Line, message.Column,
				message.RuleID, "eslint",
			))
		}
	}
	return out, true
}

func parseText(raw, workspace, cwd string) ([]Diagnostic, string, bool) {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	var out []Diagnostic
	currentESLintFile := ""
	parser := "generic"
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		if match := tsPattern.FindStringSubmatch(line); match != nil {
			parser = "typescript"
			out = append(out, makeDiagnostic(
				match[4], match[6], normalizePath(match[1], workspace, cwd),
				atoi(match[2]), atoi(match[3]), match[5], "typescript",
			))
			continue
		}
		if filepath.IsAbs(line) {
			currentESLintFile = normalizePath(line, workspace, cwd)
			continue
		}
		if match := eslintLine.FindStringSubmatch(line); match != nil && currentESLintFile != "" {
			parser = "eslint-stylish"
			out = append(out, makeDiagnostic(
				match[3], match[4], currentESLintFile,
				atoi(match[1]), atoi(match[2]), match[5], "eslint",
			))
			continue
		}
		if match := locationPattern.FindStringSubmatch(line); match != nil {
			severity := match[4]
			if severity == "" {
				severity = "error"
			}
			out = append(out, makeDiagnostic(
				severity, match[6], normalizePath(match[1], workspace, cwd),
				atoi(match[2]), atoi(match[3]), match[5], "compiler",
			))
		}
	}
	return out, parser, len(out) > 0
}

func normalizePath(raw, workspace, cwd string) string {
	raw = strings.TrimSpace(strings.Trim(raw, `"'`))
	if raw == "" {
		return ""
	}
	path := filepath.Clean(filepath.FromSlash(raw))
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	workspace = filepath.Clean(workspace)
	rel, err := filepath.Rel(workspace, path)
	if err != nil || rel == "." || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel)
}

func makeDiagnostic(
	severity, message, path string,
	line, column int,
	code, source string,
) Diagnostic {
	severity = strings.ToLower(strings.TrimSpace(severity))
	if severity != "warning" && severity != "info" {
		severity = "error"
	}
	diagnostic := Diagnostic{
		Severity: severity,
		Message:  strings.TrimSpace(message),
		Path:     path,
		Line:     line,
		Column:   column,
		Code:     strings.TrimSpace(code),
		Source:   source,
	}
	hash := sha1.Sum([]byte(fmt.Sprintf(
		"%s\x00%s\x00%s\x00%d\x00%d\x00%s",
		diagnostic.Severity, diagnostic.Message, diagnostic.Path,
		diagnostic.Line, diagnostic.Column, diagnostic.Code,
	)))
	diagnostic.ID = hex.EncodeToString(hash[:8])
	return diagnostic
}

func dedupe(input []Diagnostic) []Diagnostic {
	seen := make(map[string]struct{}, len(input))
	out := make([]Diagnostic, 0, len(input))
	for _, item := range input {
		if item.Message == "" {
			continue
		}
		if _, ok := seen[item.ID]; ok {
			continue
		}
		seen[item.ID] = struct{}{}
		out = append(out, item)
	}
	return out
}

func firstUsefulLine(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if len(line) > 500 {
				return line[:500]
			}
			return line
		}
	}
	return ""
}

func atoi(raw string) int {
	value, _ := strconv.Atoi(raw)
	return value
}
