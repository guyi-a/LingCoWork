package tools

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/guyi-a/Interview-Agent/internal/agent/scope"
	"github.com/guyi-a/Interview-Agent/internal/repository"
)

const (
	maxReadBytes    = 256 * 1024  // 256 KiB
	// maxWriteBytes 限制单次 write_file / edit_file 结果文件大小。
	// 收紧到 64 KiB 是为了避开上游 SSE 流式协议在超大 tool_call args 时的
	// 序列化 bug（会产生半个 json.RawMessage，下一轮组装历史时炸）。
	// 需要超过此限的场景用 write_file_chunked 分多次 append。
	maxWriteBytes   = 64 * 1024 // 64 KiB
	binarySniffSize = 512
)

// fsDeps is the shared closure state for all fs tools.
type fsDeps struct {
	projectRepo *repository.ProjectRepo
	convRepo    *repository.ConversationRepo
}

// resolveWorkspace returns the absolute workspace path for the current
// conversation, or a user-readable error if no workspace is mounted yet
// (so the agent knows to call create_workspace first).
func (d *fsDeps) resolveWorkspace(ctx context.Context) (string, error) {
	return resolveConversationWorkspace(ctx, d.convRepo, d.projectRepo)
}

// classifyByExt buckets a lowercase extension (with dot) into a stable
// kind string. Values are:
//
//	directory  — path is a directory (caller sets this, not returned here)
//	text       — plain text (.txt, .log, empty ext)
//	markdown   — .md / .markdown
//	code       — recognized programming/config file
//	csv        — .csv or .tsv
//	ipynb      — Jupyter notebook (JSON on disk)
//	pdf/docx/xlsx/pptx  — Office / PDF
//	image / archive / video / audio  — known binary categories
//	unknown    — everything else; binary status must be sniffed
func classifyByExt(ext string) string {
	switch ext {
	case ".md", ".markdown":
		return "markdown"
	case ".txt", ".log", "":
		return "text"
	case ".go", ".py", ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs",
		".mts", ".cts",
		".java", ".c", ".cpp", ".cc", ".cxx", ".h", ".hpp",
		".rs", ".rb", ".php", ".sh", ".bash", ".zsh",
		".sql", ".html", ".htm", ".css", ".scss", ".xml",
		".yaml", ".yml", ".toml", ".json", ".jsonc",
		".ini", ".env", ".swift", ".kt", ".dart":
		return "code"
	case ".csv", ".tsv":
		return "csv"
	case ".ipynb":
		return "ipynb"
	case ".pdf":
		return "pdf"
	case ".docx":
		return "docx"
	case ".xlsx":
		return "xlsx"
	case ".pptx":
		return "pptx"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg", ".ico":
		return "image"
	case ".zip", ".tar", ".gz", ".bz2", ".7z", ".rar":
		return "archive"
	case ".mp4", ".webm", ".mov", ".mkv", ".m4v", ".ogv":
		return "video"
	case ".mp3", ".wav", ".flac", ".ogg", ".m4a", ".aac":
		return "audio"
	default:
		return "unknown"
	}
}

// suggestedToolFor returns the recommended next tool for a given kind.
// Kinds that we can't currently read return "no_reader_available" so the
// agent knows to stop trying and tell the user.
func suggestedToolFor(kind string) string {
	switch kind {
	case "directory":
		return "list_files"
	case "text", "markdown", "code", "csv", "ipynb":
		return "read_file"
	case "pdf", "docx", "pptx", "image":
		return "extract_document_text"
	default:
		return "no_reader_available"
	}
}

// hasNullByte returns true if any byte in b is a NUL — a fast (if crude)
// heuristic for detecting binary content when we don't trust the extension.
func hasNullByte(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

// --- list_files ---

type ListFilesInput struct {
	Path string `json:"path" jsonschema:"description=Directory to list. Either an absolute local path (any location on the user's machine) or relative to the current workspace root. Default '.' = workspace root."`
}

type ListFilesEntry struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Size  int64  `json:"size,omitempty"`
	IsDir bool   `json:"is_dir"`
}

type ListFilesOutput struct {
	Path    string           `json:"path"`
	Entries []ListFilesEntry `json:"entries"`
}

func newListFilesTool(d *fsDeps) (tool.BaseTool, error) {
	fn := func(ctx context.Context, in *ListFilesInput) (*ListFilesOutput, error) {
		p := in.Path
		if p == "" {
			p = "."
		}
		// Relative paths need a workspace; absolute paths bypass that check.
		ws, wsErr := d.resolveWorkspace(ctx)
		if wsErr != nil && !filepath.IsAbs(p) {
			return nil, wsErr
		}
		abs, err := scope.ResolveRead(ws, p)
		if err != nil {
			return nil, err
		}
		dirents, err := os.ReadDir(abs)
		if err != nil {
			return nil, fmt.Errorf("read dir: %w", err)
		}
		out := &ListFilesOutput{Path: abs, Entries: make([]ListFilesEntry, 0, len(dirents))}
		for _, de := range dirents {
			info, err := de.Info()
			if err != nil {
				continue
			}
			entry := ListFilesEntry{Name: de.Name(), IsDir: de.IsDir()}
			if de.IsDir() {
				entry.Type = "dir"
			} else {
				entry.Type = "file"
				entry.Size = info.Size()
			}
			out.Entries = append(out.Entries, entry)
		}
		sort.Slice(out.Entries, func(i, j int) bool {
			a, b := out.Entries[i], out.Entries[j]
			if a.IsDir != b.IsDir {
				return a.IsDir
			}
			return a.Name < b.Name
		})
		return out, nil
	}
	return utils.InferTool(
		"list_files",
		"List directory contents. Accepts an absolute local path (anywhere on the user's machine) or a workspace-relative path (default '.' = workspace root). Only list a directory when the user explicitly names it; don't wander into the user's system on your own.",
		fn,
	)
}

// --- read_file ---

type ReadFileInput struct {
	Path   string `json:"path" jsonschema:"description=File path to read. Either an absolute local path (any location on the user's machine) or relative to the current workspace root."`
	Offset int64  `json:"offset" jsonschema:"description=Byte offset to start reading from. Default 0 (start of file). Use next_offset from a previous truncated call to continue reading."`
	Limit  int    `json:"limit" jsonschema:"description=Max bytes to read this call. 0 or unset = use default (256 KiB). Values above 262144 are clamped down. Ask for less when you only need a peek."`
}

type ReadFileOutput struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	Offset     int64  `json:"offset"`
	BytesRead  int    `json:"bytes_read"`
	NextOffset int64  `json:"next_offset"`             // where to resume; equals size when eof
	EOF        bool   `json:"eof"`                     // true if this read reached end of file
	Truncated  bool   `json:"truncated,omitempty"`     // legacy alias: content ends before EOF because limit was hit
	SizeBytes  int64  `json:"size_bytes"`
}

func newReadFileTool(d *fsDeps) (tool.BaseTool, error) {
	fn := func(ctx context.Context, in *ReadFileInput) (*ReadFileOutput, error) {
		if in.Path == "" {
			return nil, fmt.Errorf("path is required")
		}
		if in.Offset < 0 {
			return nil, fmt.Errorf("offset must be >= 0")
		}
		if in.Limit < 0 {
			return nil, fmt.Errorf("limit must be >= 0")
		}
		limit := in.Limit
		if limit == 0 || limit > maxReadBytes {
			limit = maxReadBytes
		}
		// Relative paths still need a workspace; absolute paths bypass the
		// workspace-required check. resolveWorkspace's error is only fatal
		// when the caller supplied a relative path.
		ws, wsErr := d.resolveWorkspace(ctx)
		if wsErr != nil && !filepath.IsAbs(in.Path) {
			return nil, wsErr
		}
		abs, err := scope.ResolveRead(ws, in.Path)
		if err != nil {
			return nil, err
		}
		st, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("stat: %w", err)
		}
		if st.IsDir() {
			return nil, fmt.Errorf("%q is a directory; use list_files instead", in.Path)
		}
		size := st.Size()
		if in.Offset > size {
			return nil, fmt.Errorf("offset %d exceeds file size %d", in.Offset, size)
		}
		f, err := os.Open(abs)
		if err != nil {
			return nil, fmt.Errorf("open: %w", err)
		}
		defer f.Close()
		if in.Offset > 0 {
			if _, err := f.Seek(in.Offset, 0); err != nil {
				return nil, fmt.Errorf("seek: %w", err)
			}
		}
		buf := make([]byte, limit)
		n, err := io.ReadFull(f, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return nil, fmt.Errorf("read: %w", err)
		}
		// Binary reject — only enforced for a fresh read from the head. When
		// the agent explicitly seeks past 0, we trust the intent (may be
		// continuing a previous truncated read, or slicing a known file).
		if in.Offset == 0 {
			sniffLen := n
			if sniffLen > binarySniffSize {
				sniffLen = binarySniffSize
			}
			if hasNullByte(buf[:sniffLen]) {
				kind := classifyByExt(strings.ToLower(filepath.Ext(abs)))
				suggest := suggestedToolFor(kind)
				if suggest == "read_file" || suggest == "no_reader_available" {
					return nil, fmt.Errorf(
						"file %q appears to be binary (kind=%s); call file_info for details, no supported text reader for this type",
						in.Path, kind,
					)
				}
				return nil, fmt.Errorf(
					"file %q appears to be binary (kind=%s); use %s instead",
					in.Path, kind, suggest,
				)
			}
		}
		next := in.Offset + int64(n)
		eof := next >= size
		return &ReadFileOutput{
			Path:       abs,
			Content:    string(buf[:n]),
			Offset:     in.Offset,
			BytesRead:  n,
			NextOffset: next,
			EOF:        eof,
			Truncated:  !eof,
			SizeBytes:  size,
		}, nil
	}
	return utils.InferTool(
		"read_file",
		fmt.Sprintf(
			"Read a UTF-8 text slice from a file. Accepts an absolute local path (anywhere on the user's machine) or a workspace-relative path. Reads at most %d KiB per call — for larger files, pass offset (bytes) to continue where the previous call ended (use next_offset). Set limit to cap this call's read size. Rejects binary files (only on offset=0). Returns { content, offset, bytes_read, next_offset, eof, size_bytes }.",
			maxReadBytes/1024,
		),
		fn,
	)
}

// --- file_info ---

type FileInfoInput struct {
	Path string `json:"path" jsonschema:"description=File or directory path to inspect. Absolute local path or workspace-relative."`
}

type FileInfoOutput struct {
	Path          string `json:"path"`
	Name          string `json:"name"`
	Ext           string `json:"ext,omitempty"`
	Size          int64  `json:"size"`
	IsDir         bool   `json:"is_dir"`
	IsText        bool   `json:"is_text"`
	Kind          string `json:"kind"`
	SuggestedTool string `json:"suggested_tool"`
}

func newFileInfoTool(d *fsDeps) (tool.BaseTool, error) {
	fn := func(ctx context.Context, in *FileInfoInput) (*FileInfoOutput, error) {
		if in.Path == "" {
			return nil, fmt.Errorf("path is required")
		}
		ws, wsErr := d.resolveWorkspace(ctx)
		if wsErr != nil && !filepath.IsAbs(in.Path) {
			return nil, wsErr
		}
		abs, err := scope.ResolveRead(ws, in.Path)
		if err != nil {
			return nil, err
		}
		st, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("stat: %w", err)
		}

		out := &FileInfoOutput{
			Path: abs,
			Name: filepath.Base(abs),
			Size: st.Size(),
		}

		if st.IsDir() {
			out.IsDir = true
			out.Kind = "directory"
			out.SuggestedTool = suggestedToolFor("directory")
			return out, nil
		}

		ext := strings.ToLower(filepath.Ext(abs))
		out.Ext = strings.TrimPrefix(ext, ".")
		kind := classifyByExt(ext)

		// Refine kind via a null-byte sniff for the "unknown" case, or to
		// downgrade a "text-shaped" extension whose actual content is binary
		// (rare but happens with mis-named files).
		isText := isKnownText(kind)
		if !isText && kind != "unknown" {
			// Known-binary kind (pdf/docx/image/…): trust the extension.
			out.Kind = kind
			out.IsText = false
			out.SuggestedTool = suggestedToolFor(kind)
			return out, nil
		}
		// Sniff the first few bytes to decide.
		f, err := os.Open(abs)
		if err != nil {
			return nil, fmt.Errorf("open: %w", err)
		}
		defer f.Close()
		sniff := make([]byte, binarySniffSize)
		n, _ := f.Read(sniff)
		binary := hasNullByte(sniff[:n])

		if binary {
			// A text-shaped ext that's actually binary; call it out.
			out.Kind = "binary"
			out.IsText = false
			out.SuggestedTool = suggestedToolFor("binary")
			return out, nil
		}
		out.IsText = true
		if kind == "unknown" {
			// Unknown ext but content is text — treat as generic text.
			out.Kind = "text"
			out.SuggestedTool = suggestedToolFor("text")
		} else {
			out.Kind = kind
			out.SuggestedTool = suggestedToolFor(kind)
		}
		return out, nil
	}
	return utils.InferTool(
		"file_info",
		"Inspect a file or directory: returns size, kind (text/markdown/code/csv/pdf/docx/image/directory/…), whether it's text or binary, and the recommended follow-up tool (read_file / extract_document_text / list_files / no_reader_available). Call this when unsure how to handle a path.",
		fn,
	)
}

// isKnownText tells us whether a kind is guaranteed to be text without
// needing a content sniff (used by file_info to short-circuit).
func isKnownText(kind string) bool {
	switch kind {
	case "text", "markdown", "code", "csv", "ipynb":
		return true
	}
	return false
}

// --- write_file ---

type WriteFileInput struct {
	Path    string `json:"path" jsonschema:"description=File path to write. Relative to workspace root. Parent directories are created automatically."`
	Content string `json:"content" jsonschema:"description=File content. UTF-8 text. The whole file is overwritten."`
}

type WriteFileOutput struct {
	Path      string `json:"path"`
	SizeBytes int    `json:"size_bytes"`
}

func newWriteFileTool(d *fsDeps) (tool.BaseTool, error) {
	fn := func(ctx context.Context, in *WriteFileInput) (*WriteFileOutput, error) {
		if in.Path == "" {
			return nil, fmt.Errorf("path is required")
		}
		if len(in.Content) > maxWriteBytes {
			return nil, fmt.Errorf("content too large: %d bytes (max %d)", len(in.Content), maxWriteBytes)
		}
		ws, err := d.resolveWorkspace(ctx)
		if err != nil {
			return nil, err
		}
		abs, err := scope.Resolve(ws, in.Path)
		if err != nil {
			return nil, err
		}
		if abs == strings.TrimSuffix(ws, string(filepath.Separator)) {
			return nil, fmt.Errorf("refusing to write to the workspace root")
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir parent: %w", err)
		}
		if err := os.WriteFile(abs, []byte(in.Content), 0o644); err != nil {
			return nil, fmt.Errorf("write file: %w", err)
		}
		return &WriteFileOutput{Path: abs, SizeBytes: len(in.Content)}, nil
	}
	return utils.InferTool(
		"write_file",
		"Create or fully overwrite a UTF-8 text file inside the workspace. Missing parent directories are created. Prefer edit_file for partial changes; use this only when creating a new file or rewriting the whole content. **Size cap: 64 KiB per call** — files above this must be written via write_file_chunked in 小 chunks (each append ≤ 32 KiB, recommended ≤ 15 KiB to stay well below upstream streaming limits).",
		fn,
	)
}

// --- edit_file ---

type EditFileInput struct {
	Path       string `json:"path" jsonschema:"description=File path to edit. Relative to workspace root."`
	OldString  string `json:"old_string" jsonschema:"description=Exact text to find. Must appear EXACTLY ONCE in the file when replace_all=false (otherwise the edit is rejected — add more surrounding context to make the match unique). When replace_all=true\\, may match multiple times."`
	NewString  string `json:"new_string" jsonschema:"description=Replacement text. Use empty string to delete the matched region."`
	ReplaceAll bool   `json:"replace_all" jsonschema:"description=If true\\, replace every occurrence of old_string. If false (default)\\, require old_string to appear exactly once."`
}

type EditFileOutput struct {
	Path           string `json:"path"`
	BytesBefore    int    `json:"bytes_before"`
	BytesAfter     int    `json:"bytes_after"`
	Replacements   int    `json:"replacements"`              // number of occurrences replaced
	OccurrenceLine int    `json:"occurrence_line,omitempty"` // 1-based line of the first match (single-replace mode only)
}

func newEditFileTool(d *fsDeps) (tool.BaseTool, error) {
	fn := func(ctx context.Context, in *EditFileInput) (*EditFileOutput, error) {
		if in.Path == "" {
			return nil, fmt.Errorf("path is required")
		}
		if in.OldString == "" {
			return nil, fmt.Errorf("old_string is required (use write_file to create or fully replace a file)")
		}
		if in.OldString == in.NewString {
			return nil, fmt.Errorf("old_string and new_string are identical; nothing to do")
		}
		ws, err := d.resolveWorkspace(ctx)
		if err != nil {
			return nil, err
		}
		abs, err := scope.Resolve(ws, in.Path)
		if err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(abs)
		if err != nil {
			return nil, fmt.Errorf("read file: %w", err)
		}
		content := string(raw)
		count := strings.Count(content, in.OldString)
		if count == 0 {
			return nil, fmt.Errorf("old_string not found in %q", in.Path)
		}

		var out string
		var firstLine int
		if in.ReplaceAll {
			out = strings.ReplaceAll(content, in.OldString, in.NewString)
		} else {
			if count > 1 {
				return nil, fmt.Errorf("old_string matches %d locations in %q; add more surrounding context to make it unique, or pass replace_all=true to change all", count, in.Path)
			}
			idx := strings.Index(content, in.OldString)
			firstLine = 1 + strings.Count(content[:idx], "\n")
			out = content[:idx] + in.NewString + content[idx+len(in.OldString):]
		}
		if len(out) > maxWriteBytes {
			return nil, fmt.Errorf("resulting file too large: %d bytes (max %d)", len(out), maxWriteBytes)
		}
		if err := os.WriteFile(abs, []byte(out), 0o644); err != nil {
			return nil, fmt.Errorf("write file: %w", err)
		}
		return &EditFileOutput{
			Path:           abs,
			BytesBefore:    len(raw),
			BytesAfter:     len(out),
			Replacements:   count,
			OccurrenceLine: firstLine,
		}, nil
	}
	return utils.InferTool(
		"edit_file",
		"Make an in-place edit by replacing exact text. In default mode (replace_all=false) old_string must appear EXACTLY ONCE — include enough surrounding context to make the match unique. Pass replace_all=true to replace every occurrence at once (returns the count). Use empty new_string to delete. Preferred over write_file for partial changes.",
		fn,
	)
}

// --- edit_file_lines ---

type EditFileLinesInput struct {
	Path       string `json:"path" jsonschema:"description=File path to edit. Relative to workspace root."`
	StartLine  int    `json:"start_line" jsonschema:"description=1-based line number where the replacement begins (inclusive)."`
	EndLine    int    `json:"end_line" jsonschema:"description=1-based line number where the replacement ends (inclusive). Must be >= start_line. To replace one line\\, set end_line = start_line."`
	NewContent string `json:"new_content" jsonschema:"description=Text to put in place of lines [start_line\\, end_line]. Use empty string to delete the range. Interior newlines are preserved; a trailing newline is added automatically when needed to keep the file well-formed."`
}

type EditFileLinesOutput struct {
	Path         string `json:"path"`
	BytesBefore  int    `json:"bytes_before"`
	BytesAfter   int    `json:"bytes_after"`
	LinesRemoved int    `json:"lines_removed"`
	StartLine    int    `json:"start_line"`
	EndLine      int    `json:"end_line"`
}

func newEditFileLinesTool(d *fsDeps) (tool.BaseTool, error) {
	fn := func(ctx context.Context, in *EditFileLinesInput) (*EditFileLinesOutput, error) {
		if in.Path == "" {
			return nil, fmt.Errorf("path is required")
		}
		if in.StartLine < 1 {
			return nil, fmt.Errorf("start_line must be >= 1")
		}
		if in.EndLine < in.StartLine {
			return nil, fmt.Errorf("end_line (%d) must be >= start_line (%d)", in.EndLine, in.StartLine)
		}
		ws, err := d.resolveWorkspace(ctx)
		if err != nil {
			return nil, err
		}
		abs, err := scope.Resolve(ws, in.Path)
		if err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(abs)
		if err != nil {
			return nil, fmt.Errorf("read file: %w", err)
		}
		content := string(raw)
		if content == "" {
			return nil, fmt.Errorf("file %q is empty; use write_file instead", in.Path)
		}
		lines := strings.SplitAfter(content, "\n")
		// Trailing empty element appears when content ends with "\n"; drop it
		// so total == real line count.
		if lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		total := len(lines)
		if in.StartLine > total {
			return nil, fmt.Errorf("start_line %d exceeds file line count %d", in.StartLine, total)
		}
		if in.EndLine > total {
			return nil, fmt.Errorf("end_line %d exceeds file line count %d", in.EndLine, total)
		}
		before := strings.Join(lines[:in.StartLine-1], "")
		after := strings.Join(lines[in.EndLine:], "")
		nc := in.NewContent
		// Keep file well-formed: if we still have content after the replaced
		// range, ensure the injected block ends with a newline so `after`
		// doesn't fuse onto its last line.
		if nc != "" && len(after) > 0 && !strings.HasSuffix(nc, "\n") {
			nc += "\n"
		}
		out := before + nc + after
		if len(out) > maxWriteBytes {
			return nil, fmt.Errorf("resulting file too large: %d bytes (max %d)", len(out), maxWriteBytes)
		}
		if err := os.WriteFile(abs, []byte(out), 0o644); err != nil {
			return nil, fmt.Errorf("write file: %w", err)
		}
		return &EditFileLinesOutput{
			Path:         abs,
			BytesBefore:  len(raw),
			BytesAfter:   len(out),
			LinesRemoved: in.EndLine - in.StartLine + 1,
			StartLine:    in.StartLine,
			EndLine:      in.EndLine,
		}, nil
	}
	return utils.InferTool(
		"edit_file_lines",
		"Replace a contiguous line range [start_line, end_line] (1-based, inclusive) with new_content. Use when you know the exact line numbers (e.g. from grep output or a previous read_file). Empty new_content deletes the range. To insert lines, first read_file to find an anchor and use edit_file with old_string set to that anchor — this tool does NOT do pure insertions.",
		fn,
	)
}

// --- mkdir ---

type MkdirInput struct {
	Path string `json:"path" jsonschema:"description=Directory path to create. Relative to workspace root. Intermediate directories are created automatically. No-op if already exists."`
}

type MkdirOutput struct {
	Path    string `json:"path"`
	Created bool   `json:"created"` // false if it already existed
}

func newMkdirTool(d *fsDeps) (tool.BaseTool, error) {
	fn := func(ctx context.Context, in *MkdirInput) (*MkdirOutput, error) {
		if in.Path == "" {
			return nil, fmt.Errorf("path is required")
		}
		ws, err := d.resolveWorkspace(ctx)
		if err != nil {
			return nil, err
		}
		abs, err := scope.Resolve(ws, in.Path)
		if err != nil {
			return nil, err
		}
		existed := true
		if st, err := os.Stat(abs); err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("stat: %w", err)
			}
			existed = false
		} else if !st.IsDir() {
			return nil, fmt.Errorf("%q already exists and is not a directory", in.Path)
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir: %w", err)
		}
		return &MkdirOutput{Path: abs, Created: !existed}, nil
	}
	return utils.InferTool(
		"mkdir",
		"Create a directory inside the workspace (mkdir -p semantics; no-op if it already exists). Use this before write_file when the desired parent layout doesn't exist yet.",
		fn,
	)
}
