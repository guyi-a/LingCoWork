package tools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/git-pkgs/gitignore"

	"github.com/guyi-a/Interview-Agent/internal/agent/scope"
	"github.com/guyi-a/Interview-Agent/internal/approval"
)

const (
	defaultGlobResults  = 200
	maxGlobResults      = 1000
	defaultGrepMatches  = 100
	maxGrepMatches      = 500
	maxGrepContextLines = 3
	maxSearchFiles      = 50_000
	maxSearchDirs       = 10_000
	maxSearchOutput     = 64 * 1024
	maxGrepFileBytes    = 2 * 1024 * 1024
	maxGrepLineRunes    = 500
	searchTimeout       = 60 * time.Second
)

var errSearchStopped = errors.New("search stopped after reaching a resource limit")

type searchWalkStats struct {
	Files     int
	Dirs      int
	Truncated bool
	Reason    string
}

type GlobInput struct {
	Pattern    string `json:"pattern" jsonschema:"description=Workspace-relative glob pattern such as **/*.go or web/**/*.tsx. Uses slash separators and supports **."`
	Path       string `json:"path,omitempty" jsonschema:"description=Workspace-relative directory to search. Default: workspace root."`
	MaxResults int    `json:"max_results,omitempty" jsonschema:"description=Maximum matching paths to return. Default 200; hard maximum 1000."`
}

type GlobOutput struct {
	Pattern      string   `json:"pattern"`
	Path         string   `json:"path"`
	Matches      []string `json:"matches"`
	MatchCount   int      `json:"match_count"`
	FilesScanned int      `json:"files_scanned"`
	Truncated    bool     `json:"truncated,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	DurationMs   int64    `json:"duration_ms"`
}

func newGlobTool(d *fsDeps) (tool.BaseTool, error) {
	fn := func(ctx context.Context, in *GlobInput) (*GlobOutput, error) {
		ws, start, relStart, err := resolveSearchRoot(ctx, d, in.Path)
		if err != nil {
			return nil, err
		}
		return globSearch(ctx, ws, start, relStart, in)
	}
	return utils.InferTool(
		"glob",
		"Find files inside the current workspace by path pattern. Supports ** globbing, respects nested .gitignore files, skips dependency/build directories, symlinks and sensitive credential paths, and returns workspace-relative paths. Results are bounded and may set truncated=true. Use this before grep when you know a filename or extension pattern.",
		fn,
	)
}

func globSearch(ctx context.Context, ws, start, relStart string, in *GlobInput) (*GlobOutput, error) {
	pattern := filepath.ToSlash(strings.TrimSpace(in.Pattern))
	if pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	if !doublestar.ValidatePattern(pattern) {
		return nil, fmt.Errorf("invalid glob pattern %q", in.Pattern)
	}
	limit := boundedInt(in.MaxResults, defaultGlobResults, maxGlobResults)
	out := &GlobOutput{
		Pattern: pattern,
		Path:    displaySearchPath(relStart),
		Matches: make([]string, 0, min(limit, 64)),
	}
	started := time.Now()
	searchCtx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()

	stats, err := walkSearchFiles(searchCtx, ws, start, relStart, func(relWorkspace, relBase, _ string, _ fs.DirEntry) error {
		matched, matchErr := doublestar.Match(pattern, filepath.ToSlash(relBase))
		if matchErr != nil {
			return fmt.Errorf("match glob: %w", matchErr)
		}
		if !matched {
			return nil
		}
		if len(out.Matches) >= limit {
			out.Truncated = true
			out.Reason = "maximum results reached"
			return errSearchStopped
		}
		if searchOutputSize(out.Matches)+len(relWorkspace)+4 > maxSearchOutput {
			out.Truncated = true
			out.Reason = "output size limit reached"
			return errSearchStopped
		}
		out.Matches = append(out.Matches, filepath.ToSlash(relWorkspace))
		return nil
	})
	out.DurationMs = time.Since(started).Milliseconds()
	out.MatchCount = len(out.Matches)
	out.FilesScanned = stats.Files
	mergeSearchTruncation(&out.Truncated, &out.Reason, stats)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil && !errors.Is(err, errSearchStopped) {
		if errors.Is(searchCtx.Err(), context.DeadlineExceeded) {
			out.Truncated = true
			out.Reason = "search timeout reached"
			return out, nil
		}
		return nil, err
	}
	return out, nil
}

type GrepInput struct {
	Pattern         string `json:"pattern" jsonschema:"description=Regular expression to search for. Set fixed_string=true to treat it as literal text."`
	Path            string `json:"path,omitempty" jsonschema:"description=Workspace-relative directory to search. Default: workspace root."`
	Glob            string `json:"glob,omitempty" jsonschema:"description=Optional file glob such as **/*.go or *.{ts\\,tsx}."`
	CaseInsensitive bool   `json:"case_insensitive,omitempty" jsonschema:"description=Match without case sensitivity."`
	FixedString     bool   `json:"fixed_string,omitempty" jsonschema:"description=Treat pattern as literal text instead of a regular expression."`
	ContextLines    int    `json:"context_lines,omitempty" jsonschema:"description=Lines of context before and after each match. Range 0-3."`
	MaxMatches      int    `json:"max_matches,omitempty" jsonschema:"description=Maximum matching lines to return. Default 100; hard maximum 500."`
}

type GrepMatch struct {
	Path   string   `json:"path"`
	Line   int      `json:"line"`
	Column int      `json:"column"`
	Text   string   `json:"text"`
	Before []string `json:"before,omitempty"`
	After  []string `json:"after,omitempty"`
}

type GrepOutput struct {
	Pattern      string      `json:"pattern"`
	Path         string      `json:"path"`
	Glob         string      `json:"glob,omitempty"`
	Matches      []GrepMatch `json:"matches"`
	MatchCount   int         `json:"match_count"`
	FilesScanned int         `json:"files_scanned"`
	FilesSkipped int         `json:"files_skipped,omitempty"`
	Truncated    bool        `json:"truncated,omitempty"`
	Reason       string      `json:"reason,omitempty"`
	DurationMs   int64       `json:"duration_ms"`
}

func newGrepTool(d *fsDeps) (tool.BaseTool, error) {
	fn := func(ctx context.Context, in *GrepInput) (*GrepOutput, error) {
		ws, start, relStart, err := resolveSearchRoot(ctx, d, in.Path)
		if err != nil {
			return nil, err
		}
		return grepSearch(ctx, ws, start, relStart, in)
	}
	return utils.InferTool(
		"grep",
		"Search text files inside the current workspace and return structured workspace-relative path, line, column and context. The pattern is a Go regular expression unless fixed_string=true. Respects nested .gitignore files and skips binary, oversized, dependency/build, symlink and sensitive credential files. Results are bounded and may set truncated=true.",
		fn,
	)
}

func grepSearch(ctx context.Context, ws, start, relStart string, in *GrepInput) (*GrepOutput, error) {
	rawPattern := in.Pattern
	if rawPattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	compiledPattern := rawPattern
	if in.FixedString {
		compiledPattern = regexp.QuoteMeta(compiledPattern)
	}
	if in.CaseInsensitive {
		compiledPattern = "(?i)" + compiledPattern
	}
	re, err := regexp.Compile(compiledPattern)
	if err != nil {
		return nil, fmt.Errorf("invalid grep pattern: %w", err)
	}
	fileGlob := filepath.ToSlash(strings.TrimSpace(in.Glob))
	if fileGlob != "" && !doublestar.ValidatePattern(fileGlob) {
		return nil, fmt.Errorf("invalid file glob %q", in.Glob)
	}
	contextLines := in.ContextLines
	if contextLines < 0 {
		return nil, fmt.Errorf("context_lines must be >= 0")
	}
	if contextLines > maxGrepContextLines {
		contextLines = maxGrepContextLines
	}
	limit := boundedInt(in.MaxMatches, defaultGrepMatches, maxGrepMatches)
	out := &GrepOutput{
		Pattern: rawPattern,
		Path:    displaySearchPath(relStart),
		Glob:    fileGlob,
		Matches: make([]GrepMatch, 0, min(limit, 32)),
	}
	started := time.Now()
	searchCtx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()

	stats, walkErr := walkSearchFiles(searchCtx, ws, start, relStart, func(relWorkspace, relBase, abs string, entry fs.DirEntry) error {
		if fileGlob != "" {
			matched, matchErr := doublestar.Match(fileGlob, filepath.ToSlash(relBase))
			if matchErr != nil {
				return fmt.Errorf("match file glob: %w", matchErr)
			}
			if !matched {
				return nil
			}
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			out.FilesSkipped++
			return nil
		}
		if info.Size() > maxGrepFileBytes {
			out.FilesSkipped++
			return nil
		}
		raw, readErr := os.ReadFile(abs)
		if readErr != nil {
			out.FilesSkipped++
			return nil
		}
		sniffLen := min(len(raw), binarySniffSize)
		if hasNullByte(raw[:sniffLen]) || !utf8.Valid(raw) {
			out.FilesSkipped++
			return nil
		}
		lines := strings.Split(string(raw), "\n")
		for i, line := range lines {
			loc := re.FindStringIndex(line)
			if loc == nil {
				continue
			}
			if len(out.Matches) >= limit {
				out.Truncated = true
				out.Reason = "maximum matches reached"
				return errSearchStopped
			}
			match := GrepMatch{
				Path:   filepath.ToSlash(relWorkspace),
				Line:   i + 1,
				Column: utf8.RuneCountInString(line[:loc[0]]) + 1,
				Text:   truncateRunes(line, maxGrepLineRunes),
			}
			if contextLines > 0 {
				from := max(0, i-contextLines)
				to := min(len(lines), i+contextLines+1)
				match.Before = truncateLines(lines[from:i])
				match.After = truncateLines(lines[i+1 : to])
			}
			if grepMatchOutputSize(match)+grepOutputSize(out.Matches) > maxSearchOutput {
				out.Truncated = true
				out.Reason = "output size limit reached"
				return errSearchStopped
			}
			out.Matches = append(out.Matches, match)
		}
		return nil
	})
	out.DurationMs = time.Since(started).Milliseconds()
	out.MatchCount = len(out.Matches)
	out.FilesScanned = stats.Files
	mergeSearchTruncation(&out.Truncated, &out.Reason, stats)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if walkErr != nil && !errors.Is(walkErr, errSearchStopped) {
		if errors.Is(searchCtx.Err(), context.DeadlineExceeded) {
			out.Truncated = true
			out.Reason = "search timeout reached"
			return out, nil
		}
		return nil, walkErr
	}
	return out, nil
}

func resolveSearchRoot(ctx context.Context, d *fsDeps, rawPath string) (ws, start, relStart string, err error) {
	ws, err = d.resolveWorkspace(ctx)
	if err != nil {
		return "", "", "", err
	}
	ws, err = filepath.EvalSymlinks(filepath.Clean(ws))
	if err != nil {
		return "", "", "", fmt.Errorf("resolve workspace: %w", err)
	}
	path := strings.TrimSpace(rawPath)
	if path == "" {
		path = "."
	}
	start, err = scope.Resolve(ws, path)
	if err != nil {
		return "", "", "", fmt.Errorf("search path: %w", err)
	}
	info, err := os.Stat(start)
	if err != nil {
		return "", "", "", fmt.Errorf("search path: %w", err)
	}
	if !info.IsDir() {
		return "", "", "", fmt.Errorf("search path %q is not a directory", rawPath)
	}
	relStart, err = filepath.Rel(ws, start)
	if err != nil {
		return "", "", "", fmt.Errorf("relative search path: %w", err)
	}
	if ignoredSearchPath(filepath.ToSlash(relStart)) {
		return "", "", "", fmt.Errorf("search path %q is excluded from workspace search", rawPath)
	}
	return ws, start, relStart, nil
}

type searchVisitor func(relWorkspace, relBase, abs string, entry fs.DirEntry) error

func walkSearchFiles(
	ctx context.Context,
	ws, start, relStart string,
	visit searchVisitor,
) (searchWalkStats, error) {
	stats := searchWalkStats{}
	matcher := gitignore.New("")
	matcher.AddFromFile(filepath.Join(ws, ".git", "info", "exclude"), "")
	matcher.AddFromFile(filepath.Join(ws, ".gitignore"), "")

	// A search rooted below the workspace still needs ignore files from every
	// ancestor directory, because their rules apply to the subtree.
	if relStart != "." && relStart != "" {
		parts := strings.Split(filepath.ToSlash(relStart), "/")
		for i := 1; i < len(parts); i++ {
			dir := strings.Join(parts[:i], "/")
			matcher.AddFromFile(filepath.Join(ws, filepath.FromSlash(dir), ".gitignore"), dir)
		}
	}

	var walk func(absDir, relWorkspace string) error
	walk = func(absDir, relWorkspace string) error {
		if err := ctx.Err(); err != nil {
			stats.Truncated = true
			stats.Reason = "search timeout or cancellation"
			return errSearchStopped
		}
		stats.Dirs++
		if stats.Dirs > maxSearchDirs {
			stats.Truncated = true
			stats.Reason = "directory scan limit reached"
			return errSearchStopped
		}
		if relWorkspace != "" && relWorkspace != "." {
			matcher.AddFromFile(filepath.Join(absDir, ".gitignore"), filepath.ToSlash(relWorkspace))
		}
		entries, err := os.ReadDir(absDir)
		if err != nil {
			return nil // one unreadable directory must not fail the whole search
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				stats.Truncated = true
				stats.Reason = "search timeout or cancellation"
				return errSearchStopped
			}
			name := entry.Name()
			entryRel := name
			if relWorkspace != "" && relWorkspace != "." {
				entryRel = filepath.Join(relWorkspace, name)
			}
			entryRelSlash := filepath.ToSlash(entryRel)
			if hardIgnoredSearchEntry(entryRelSlash, entry) {
				continue
			}
			if matcher.MatchPath(entryRelSlash, entry.IsDir()) {
				continue
			}
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			abs := filepath.Join(absDir, name)
			if entry.IsDir() {
				if err := walk(abs, entryRel); err != nil {
					return err
				}
				continue
			}
			stats.Files++
			if stats.Files > maxSearchFiles {
				stats.Truncated = true
				stats.Reason = "file scan limit reached"
				return errSearchStopped
			}
			relBase, err := filepath.Rel(start, abs)
			if err != nil {
				continue
			}
			if err := visit(entryRel, relBase, abs, entry); err != nil {
				return err
			}
		}
		return nil
	}

	startRel := relStart
	if startRel == "." {
		startRel = ""
	}
	err := walk(start, startRel)
	return stats, err
}

func hardIgnoredSearchEntry(rel string, _ fs.DirEntry) bool {
	return ignoredSearchPath(rel)
}

func ignoredSearchPath(rel string) bool {
	cleaned := strings.Trim(filepath.ToSlash(rel), "/")
	for _, segment := range strings.Split(cleaned, "/") {
		switch segment {
		case ".git", ".lingcowork", ".workspace", ".myflicker",
			"node_modules", "vendor", "dist", "build", "target",
			".next", ".nuxt", "coverage":
			return true
		}
	}
	_, sensitive := approval.PathIsSensitive(cleaned)
	return sensitive
}

func boundedInt(value, defaultValue, maxValue int) int {
	if value <= 0 {
		return defaultValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func displaySearchPath(rel string) string {
	if rel == "" || rel == "." {
		return "."
	}
	return filepath.ToSlash(rel)
}

func mergeSearchTruncation(truncated *bool, reason *string, stats searchWalkStats) {
	if !stats.Truncated {
		return
	}
	*truncated = true
	if *reason == "" {
		*reason = stats.Reason
	}
}

func searchOutputSize(paths []string) int {
	size := 0
	for _, path := range paths {
		size += len(path) + 4
	}
	return size
}

func grepOutputSize(matches []GrepMatch) int {
	size := 0
	for _, match := range matches {
		size += grepMatchOutputSize(match)
	}
	return size
}

func grepMatchOutputSize(match GrepMatch) int {
	size := len(match.Path) + len(match.Text) + 48
	for _, line := range match.Before {
		size += len(line) + 4
	}
	for _, line := range match.After {
		size += len(line) + 4
	}
	return size
}

func truncateLines(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = truncateRunes(line, maxGrepLineRunes)
	}
	return out
}

func truncateRunes(value string, maxRunes int) string {
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxRunes]) + "…"
}
