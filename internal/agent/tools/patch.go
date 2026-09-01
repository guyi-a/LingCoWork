package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/guyi-a/Interview-Agent/internal/agent/scope"
)

const (
	maxPatchBytes      = 64 * 1024
	maxPatchTargetSize = 4 * 1024 * 1024
)

type ApplyPatchInput struct {
	Path  string `json:"path" jsonschema:"description=Existing UTF-8 text file to modify. Workspace-relative or an absolute path inside the workspace."`
	Patch string `json:"patch" jsonschema:"description=One or more context hunks. Start each hunk with @@ (an optional label may follow). Prefix unchanged context lines with one space\\, removed lines with -\\, and added lines with +. Every hunk must contain a change and enough old/context lines to match exactly once."`
	// ObservedState is the observed_state read_file returned for this file.
	// When present, the patch is rejected if the file changed since you read
	// it, so an edit based on a stale read is caught before the fuzzy fallback
	// could apply to a version you never saw.
	ObservedState string `json:"observed_state,omitempty" jsonschema:"description=Echo the observed_state you got from read_file for this file\\, so the patch is rejected if the file changed since you read it."`
}

type ApplyPatchOutput struct {
	Path        string                 `json:"path"`
	Hunks       int                    `json:"hunks"`
	HunkDetails []ApplyPatchHunkOutput `json:"hunk_details"`
	Additions   int                    `json:"additions"`
	Deletions   int                    `json:"deletions"`
	BytesBefore int                    `json:"bytes_before"`
	BytesAfter  int                    `json:"bytes_after"`
	// FuzzyHunks counts hunks that were applied via the whitespace-tolerant
	// fallback rather than an exact context match.
	FuzzyHunks int `json:"fuzzy_hunks,omitempty"`
}

type ApplyPatchHunkOutput struct {
	Index     int `json:"index"`
	Line      int `json:"line"`
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
	// Fuzzy is true when this hunk landed only after the conservative
	// whitespace-tolerant fallback (not an exact context match).
	Fuzzy bool `json:"fuzzy,omitempty"`
}

type contextPatchHunk struct {
	oldLines  []string
	newLines  []string
	additions int
	deletions int
}

type appliedPatchHunk struct {
	line      int
	additions int
	deletions int
	fuzzy     bool
}

func newApplyPatchTool(d *fsDeps) (tool.BaseTool, error) {
	fn := func(ctx context.Context, in *ApplyPatchInput) (*ApplyPatchOutput, error) {
		if strings.TrimSpace(in.Path) == "" {
			return nil, fmt.Errorf("path is required")
		}
		if strings.TrimSpace(in.Patch) == "" {
			return nil, fmt.Errorf("patch is required")
		}
		if len(in.Patch) > maxPatchBytes {
			return nil, fmt.Errorf("patch too large: %d bytes (max %d)", len(in.Patch), maxPatchBytes)
		}
		hunks, err := parseContextPatch(in.Patch)
		if err != nil {
			return nil, err
		}
		ws, err := d.resolveWorkspace(ctx)
		if err != nil {
			return nil, err
		}
		return applyPatchFile(ctx, ws, in, hunks)
	}
	return utils.InferTool(
		"apply_patch",
		"Apply one or more exact context hunks to an existing UTF-8 text file inside the workspace. Format: start every hunk with @@, then prefix context with a space, removals with -, and additions with +. All hunks are validated in memory before one atomic write; missing or ambiguous context rejects the entire patch and leaves the file unchanged. If an exact match is not found, a conservative whitespace-tolerant fallback anchors on a distinctive line and applies only when the match is unambiguous (reported as fuzzy); otherwise the whole patch is rejected. Use this as the primary tool for partial code edits. Use write_file for new/full files and rm for deletion. If you previously read_file this path, pass its observed_state so the patch is rejected when the file changed since you read it.",
		fn,
	)
}

func applyPatchFile(
	ctx context.Context,
	ws string,
	in *ApplyPatchInput,
	hunks []contextPatchHunk,
) (*ApplyPatchOutput, error) {
	abs, err := scope.Resolve(ws, in.Path)
	if err != nil {
		return nil, err
	}
	if abs == filepath.Clean(ws) {
		return nil, fmt.Errorf("refusing to patch the workspace root")
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat target: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("patch target %q is not a regular file", in.Path)
	}
	if info.Size() > maxPatchTargetSize {
		return nil, fmt.Errorf("patch target too large: %d bytes (max %d)", info.Size(), maxPatchTargetSize)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read target: %w", err)
	}
	if err := verifyObservedState(abs, in.Path, "patch", in.ObservedState); err != nil {
		return nil, err
	}
	sniffLen := min(len(raw), binarySniffSize)
	if hasNullByte(raw[:sniffLen]) || !utf8.Valid(raw) {
		return nil, fmt.Errorf("patch target %q is not UTF-8 text", in.Path)
	}
	next, applied, err := applyContextPatch(ctx, raw, hunks)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(raw, next) {
		return nil, fmt.Errorf("patch produces no change")
	}
	if len(next) > maxPatchTargetSize {
		return nil, fmt.Errorf("patched file too large: %d bytes (max %d)", len(next), maxPatchTargetSize)
	}
	if err := atomicReplaceIfUnchanged(ctx, abs, raw, next, info.Mode().Perm()); err != nil {
		return nil, err
	}
	displayRoot := filepath.Clean(ws)
	if canonical, evalErr := filepath.EvalSymlinks(displayRoot); evalErr == nil {
		displayRoot = canonical
	}
	rel, err := filepath.Rel(displayRoot, abs)
	if err != nil {
		rel = in.Path
	}
	out := &ApplyPatchOutput{
		Path:        filepath.ToSlash(rel),
		Hunks:       len(applied),
		HunkDetails: make([]ApplyPatchHunkOutput, 0, len(applied)),
		BytesBefore: len(raw),
		BytesAfter:  len(next),
	}
	for i, hunk := range applied {
		out.Additions += hunk.additions
		out.Deletions += hunk.deletions
		hd := ApplyPatchHunkOutput{
			Index:     i + 1,
			Line:      hunk.line,
			Additions: hunk.additions,
			Deletions: hunk.deletions,
			Fuzzy:     hunk.fuzzy,
		}
		if hunk.fuzzy {
			out.FuzzyHunks++
		}
		out.HunkDetails = append(out.HunkDetails, hd)
	}
	return out, nil
}

func parseContextPatch(patch string) ([]contextPatchHunk, error) {
	normalized := strings.ReplaceAll(patch, "\r\n", "\n")
	normalized = strings.TrimSuffix(normalized, "\n")
	lines := strings.Split(normalized, "\n")
	hunks := make([]contextPatchHunk, 0, 2)
	var current *contextPatchHunk

	flush := func() error {
		if current == nil {
			return nil
		}
		if current.additions == 0 && current.deletions == 0 {
			return fmt.Errorf("hunk %d contains no additions or deletions", len(hunks)+1)
		}
		if len(current.oldLines) == 0 {
			return fmt.Errorf("hunk %d has no old/context lines; include a unique context line for insertions", len(hunks)+1)
		}
		hunks = append(hunks, *current)
		current = nil
		return nil
	}

	for lineNo, line := range lines {
		if line == "*** End Patch" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		if strings.HasPrefix(line, "@@") {
			if err := flush(); err != nil {
				return nil, err
			}
			current = &contextPatchHunk{}
			continue
		}
		if current == nil {
			if line == "*** Begin Patch" || line == "*** End Patch" ||
				strings.HasPrefix(line, "*** Update File:") {
				continue
			}
			return nil, fmt.Errorf("patch line %d is outside a hunk; start with @@", lineNo+1)
		}
		if line == `\ No newline at end of file` {
			continue
		}
		if line == "" {
			return nil, fmt.Errorf("patch line %d has no prefix; use one space for an empty context line", lineNo+1)
		}
		text := line[1:]
		switch line[0] {
		case ' ':
			current.oldLines = append(current.oldLines, text)
			current.newLines = append(current.newLines, text)
		case '-':
			current.oldLines = append(current.oldLines, text)
			current.deletions++
		case '+':
			current.newLines = append(current.newLines, text)
			current.additions++
		default:
			return nil, fmt.Errorf("patch line %d has invalid prefix %q", lineNo+1, line[:1])
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(hunks) == 0 {
		return nil, fmt.Errorf("patch contains no hunks")
	}
	return hunks, nil
}

func applyContextPatch(ctx context.Context, raw []byte, hunks []contextPatchHunk) ([]byte, []appliedPatchHunk, error) {
	text := string(raw)
	eol := "\n"
	if strings.Contains(text, "\r\n") {
		eol = "\r\n"
		text = strings.ReplaceAll(text, "\r\n", "\n")
	}
	trailingNewline := strings.HasSuffix(text, "\n")
	if trailingNewline {
		text = strings.TrimSuffix(text, "\n")
	}
	lines := strings.Split(text, "\n")
	if len(raw) == 0 {
		lines = nil
	}
	applied := make([]appliedPatchHunk, 0, len(hunks))

	for i, hunk := range hunks {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		fuzzy := false
		positions := matchingLineRanges(lines, hunk.oldLines)
		var at int
		switch {
		case len(positions) == 1:
			at = positions[0]
		case len(positions) > 1:
			return nil, nil, fmt.Errorf("hunk %d context matches %d locations; include more surrounding context", i+1, len(positions))
		case len(positions) == 0:
			// Exact match failed — try a conservative, whitespace-tolerant fuzzy
			// match that anchors on a distinctive line. It must be unambiguously
			// ahead of any rival window and score well, else we still reject.
			freq := lineFreq(lines)
			pos, score, totalWeight, hasAnchor, ok := bestMatchPosition(lines, hunk.oldLines, freq)
			if !ok || !hasAnchor || float64(score) < 0.55*float64(totalWeight) {
				return nil, nil, fmt.Errorf("hunk %d context was not found; re-read the file and regenerate the patch", i+1)
			}
			at = pos
			fuzzy = true
		}
		replacement := append([]string(nil), hunk.newLines...)
		next := make([]string, 0, len(lines)-len(hunk.oldLines)+len(replacement))
		next = append(next, lines[:at]...)
		next = append(next, replacement...)
		next = append(next, lines[at+len(hunk.oldLines):]...)
		lines = next
		applied = append(applied, appliedPatchHunk{
			line:      at + 1,
			additions: hunk.additions,
			deletions: hunk.deletions,
			fuzzy:     fuzzy,
		})
	}

	result := strings.Join(lines, eol)
	if trailingNewline {
		result += eol
	}
	return []byte(result), applied, nil
}

func matchingLineRanges(lines, target []string) []int {
	if len(target) == 0 || len(target) > len(lines) {
		return nil
	}
	var positions []int
	for i := 0; i <= len(lines)-len(target); i++ {
		match := true
		for j := range target {
			if lines[i+j] != target[j] {
				match = false
				break
			}
		}
		if match {
			positions = append(positions, i)
		}
	}
	return positions
}

// lineEqForMatch compares two code lines for a fuzzy (whitespace-tolerant)
// match: trailing whitespace is ignored, but leading indentation is kept so a
// hunk can't slide onto a differently-indented block.
func lineEqForMatch(a, b string) bool {
	return strings.TrimRight(a, " \t") == strings.TrimRight(b, " \t")
}

// lineFreq counts how often each exact line appears, used to weight distinctive
// (rare) lines higher so a fuzzy match anchors on something unique rather than
// on a common line like a brace or `return`.
func lineFreq(lines []string) map[string]int {
	freq := make(map[string]int)
	for _, l := range lines {
		freq[l]++
	}
	return freq
}

// lineDistinctValue returns how strong an anchor a line is: lines that appear
// once in the file are the most distinctive, repeated lines are weak.
func lineDistinctValue(line string, freq map[string]int) int {
	switch {
	case freq[line] <= 1:
		return 4
	case freq[line] <= 2:
		return 3
	case freq[line] <= 4:
		return 2
	default:
		return 1
	}
}

// bestMatchPosition finds the best fuzzy position for oldLines within lines,
// scoring each candidate window by weighted line matches. It reports the best
// position, the weighted score, the total possible weight, whether the best
// position is anchored on at least one distinctive line, and whether the best
// is unambiguously ahead of the runner-up (rejecting a tie so we never edit a
// possibly-wrong block).
func bestMatchPosition(
	lines, oldLines []string,
	freq map[string]int,
) (pos int, score, totalWeight int, hasAnchor, ok bool) {
	if len(oldLines) == 0 || len(oldLines) > len(lines) {
		return 0, 0, 0, false, false
	}
	weights := make([]int, len(oldLines))
	for j, ol := range oldLines {
		weights[j] = lineDistinctValue(ol, freq)
		totalWeight += weights[j]
	}
	bestPos, best, second := -1, -1, -1
	bestAnchors := 0
	for i := 0; i <= len(lines)-len(oldLines); i++ {
		s := 0
		anchors := 0
		for j, ol := range oldLines {
			if lineEqForMatch(lines[i+j], ol) {
				s += weights[j]
				if weights[j] >= 2 {
					anchors++
				}
			}
		}
		if s > best {
			second = best
			best = s
			bestPos = i
			bestAnchors = anchors
		} else if s > second {
			second = s
		}
	}
	if bestPos < 0 || best <= 0 {
		return 0, 0, 0, false, false
	}
	// Reject a tie: if a second window scores the same, moving the edit there
	// could hit the wrong block, so fail and ask for more context instead.
	if best-second < 1 {
		return 0, 0, 0, false, false
	}
	return bestPos, best, totalWeight, bestAnchors > 0, true
}

func atomicReplaceIfUnchanged(
	ctx context.Context,
	path string,
	original, next []byte,
	mode os.FileMode,
) (retErr error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".lingcowork-patch-*")
	if err != nil {
		return fmt.Errorf("create patch temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if retErr != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("preserve file mode: %w", err)
	}
	if _, err := tmp.Write(next); err != nil {
		return fmt.Errorf("write patch temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync patch temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close patch temp file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("re-read patch target: %w", err)
	}
	if !bytes.Equal(current, original) {
		return fmt.Errorf("patch conflict: target changed after it was read")
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace patch target: %w", err)
	}
	return nil
}
