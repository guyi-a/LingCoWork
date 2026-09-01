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
	maxReadBytes = 256 * 1024 // 256 KiB
	// maxWriteBytes limits one-shot whole-file writes.
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

// resolveWorkspace returns the user-selected workspace path for the current
// conversation, or a user-readable error if no folder has been selected.
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

// observedState returns a cheap fingerprint of a file's identity for
// optimistic-concurrency checks. The model reads a file, receives this value,
// and passes it back on write/edit so the tool can reject an edit based on a
// stale view (another agent/process changed the file in between). size +
// mtimeNs is the same stat-cache heuristic git uses; it needs no content read
// and changes whenever the file is rewritten (LingCoWork's own writes and
// os.Rename both bump mtime). Returns "" when the file does not exist.
func observedState(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", st.Size(), st.ModTime().UnixNano())
}

// verifyObservedState rejects a write/edit based on a stale read. When the
// model passes no observed_state it is a no-op (create-new or deliberate
// overwrite). When it passes a version, the file must still be at that
// version — otherwise the operation is a conflict the model must resolve by
// re-reading. `op` is "write" or "patch", used only in the error text.
func verifyObservedState(abs, displayPath, op, observed string) error {
	if observed == "" {
		return nil
	}
	cur := observedState(abs)
	if cur == "" {
		return fmt.Errorf(
			"%s conflict: %q no longer exists (it existed when you read it); re-read_file%s",
			op, displayPath, conflictRecreateHint(op),
		)
	}
	if cur != observed {
		return fmt.Errorf(
			"%s conflict: %q changed since you read it (size/mtime differ), so it may contain edits you have not seen; call read_file to get the latest observed_state and re-apply",
			op, displayPath,
		)
	}
	return nil
}

func conflictRecreateHint(op string) string {
	if op == "write" {
		return ", or omit observed_state to create it fresh"
	}
	return ""
}

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
	NextOffset int64  `json:"next_offset"`         // where to resume; equals size when eof
	EOF        bool   `json:"eof"`                 // true if this read reached end of file
	Truncated  bool   `json:"truncated,omitempty"` // legacy alias: content ends before EOF because limit was hit
	SizeBytes  int64  `json:"size_bytes"`
	// ObservedState is a size+mtime fingerprint of the file at read time. Echo
	// it back on write_file / apply_patch so the tool can reject an edit based
	// on a stale read (conflict guard).
	ObservedState string `json:"observed_state,omitempty"`
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
			Path:          abs,
			Content:       string(buf[:n]),
			Offset:        in.Offset,
			BytesRead:     n,
			NextOffset:    next,
			EOF:           eof,
			Truncated:     !eof,
			SizeBytes:     size,
			ObservedState: fmt.Sprintf("%d:%d", size, st.ModTime().UnixNano()),
		}, nil
	}
	return utils.InferTool(
		"read_file",
		fmt.Sprintf(
			"Read a UTF-8 text slice from a file. Accepts an absolute local path (anywhere on the user's machine) or a workspace-relative path. Reads at most %d KiB per call — for larger files, pass offset (bytes) to continue where the previous call ended (use next_offset). Set limit to cap this call's read size. Rejects binary files (only on offset=0). Returns { content, offset, bytes_read, next_offset, eof, size_bytes, observed_state }. Echo observed_state back on write_file / apply_patch to guard against editing a stale version of the file.",
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
	// ObservedState is the observed_state read_file returned for this file.
	// When present, the tool verifies the file is still at that version before
	// overwriting, so an edit based on a stale read is rejected instead of
	// clobbering changes you did not see. Leave empty to create a new file or
	// to deliberately overwrite without a guard.
	ObservedState string `json:"observed_state,omitempty" jsonschema:"description=Echo the observed_state you got from read_file for this file\\, so the write is rejected if the file changed since you read it."`
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
		if err := verifyObservedState(abs, in.Path, "write", in.ObservedState); err != nil {
			return nil, err
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
		"Create or fully overwrite a UTF-8 text file inside the workspace. Missing parent directories are created. Prefer apply_patch for partial changes; use this only when creating a new file or rewriting the whole content. **Size cap: 64 KiB per call** — files above this must be written via write_file_chunked in small chunks (each append ≤ 32 KiB, recommended ≤ 15 KiB to stay well below upstream streaming limits). If you previously read_file this path, pass its observed_state so the write is rejected when the file changed since you read it.",
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
