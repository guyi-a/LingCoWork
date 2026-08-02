package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/guyi-a/Interview-Agent/internal/agent/scope"
)

const (
	chunkedWriteSessionDir = ".chunked_write_sessions"
	chunkedWriteTTL        = 12 * time.Hour
	// maxChunkBytes 限制单次 start/append 允许的 content 大小。
	// 收紧到 32 KiB 是为了避开上游 SSE 流式协议在超大 tool_call args 时的
	// 序列化 bug（半个 json.RawMessage → 下一轮组装历史时 unmarshal 炸）。
	// 建议 LLM 每段控制在 ≤ 15 KiB，32 KiB 是硬上限。
	maxChunkBytes        = 32 * 1024        // 32 KiB per append call
	maxChunkedWriteBytes = 10 * 1024 * 1024 // 10 MiB final file cap
)

type ChunkedWriteInput struct {
	Mode      string `json:"mode" jsonschema:"description=Write mode. Exactly one of: start / append / finish / abort. Always required — every call must carry it\\, including every append."`
	Path      string `json:"path,omitempty" jsonschema:"description=Target file path relative to workspace root. Required when mode=start."`
	Content   string `json:"content,omitempty" jsonschema:"description=Content chunk to append. Required when mode=append; optional initial content when mode=start."`
	SessionID string `json:"session_id,omitempty" jsonschema:"description=Write session id returned by mode=start. Required for append\\, finish\\, and abort."`
}

type ChunkedWriteOutput struct {
	Mode          string `json:"mode"`
	SessionID     string `json:"session_id,omitempty"`
	Path          string `json:"path,omitempty"`
	SizeBytes     int64  `json:"size_bytes"`
	ChunkCount    int    `json:"chunk_count"`
	ExistedBefore bool   `json:"existed_before,omitempty"`
	Message       string `json:"message"`
}

type chunkedWriteSession struct {
	Path          string    `json:"path"`
	TempPath      string    `json:"temp_path"`
	ExistedBefore bool      `json:"existed_before"`
	UpdatedAt     time.Time `json:"updated_at"`
	BytesWritten  int64     `json:"bytes_written"`
	ChunkCount    int       `json:"chunk_count"`
}

func newChunkedWriteFileTool(d *fsDeps) (tool.BaseTool, error) {
	fn := func(ctx context.Context, in *ChunkedWriteInput) (*ChunkedWriteOutput, error) {
		mode, err := resolveChunkedMode(in)
		if err != nil {
			return nil, err
		}
		switch mode {
		case "start":
			return chunkedWriteStart(ctx, d, in)
		case "append":
			return chunkedWriteAppend(ctx, d, in)
		case "finish":
			return chunkedWriteFinish(ctx, d, in)
		case "abort":
			return chunkedWriteAbort(ctx, d, in)
		default:
			return nil, fmt.Errorf("mode must be one of start, append, finish, abort")
		}
	}
	return utils.InferTool(
		"write_file_chunked",
		"Write a large UTF-8 text file inside the workspace in ordered chunks. Flow: start with path, append multiple content chunks, then finish to atomically save the file. Use abort to cancel and clean up. Prefer regular write_file for files under about 200 lines. **Chunk size cap: 32 KiB per call**; strongly recommend keeping each append ≤ 15 KiB to stay well below upstream streaming limits — larger chunks may fail with json serialization errors. For a 100 KB file, prefer 8-10 appends of ~10 KiB each over 3-4 appends of 30 KiB.",
		fn,
	)
}

// resolveChunkedMode recovers the mode when the model leaves it out.
//
// A dropped mode is not a harmless error here. The call is rejected, but a
// rejected append reads much like any other failed step in the transcript, so
// the model tends to carry on with the next chunk rather than re-send the one
// that bounced — and finish then saves a file with a hole in the middle that
// nobody notices until they read it. The argument shape is unambiguous in
// every case but one, so prefer honoring the obvious intent over being right
// about the schema.
func resolveChunkedMode(in *ChunkedWriteInput) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	switch mode {
	case "start", "append", "finish", "abort":
		return mode, nil
	case "":
		// finish and abort never carry content, and append is the only mode
		// that needs a session, so content plus a session can only be one.
		switch {
		case strings.TrimSpace(in.SessionID) != "" && in.Content != "":
			return "append", nil
		case strings.TrimSpace(in.SessionID) == "" && strings.TrimSpace(in.Path) != "":
			return "start", nil
		}
		return "", fmt.Errorf("mode is missing and cannot be inferred from the other arguments: " +
			"pass mode=finish to save the file, or mode=abort to discard it")
	default:
		return "", fmt.Errorf("mode must be one of start, append, finish, abort (got %q)", in.Mode)
	}
}

func chunkedWriteStart(ctx context.Context, d *fsDeps, in *ChunkedWriteInput) (*ChunkedWriteOutput, error) {
	if strings.TrimSpace(in.Path) == "" {
		return nil, fmt.Errorf("path is required when mode=start")
	}
	if len([]byte(in.Content)) > maxChunkBytes {
		return nil, fmt.Errorf("initial content chunk too large: %d bytes (max %d). "+
			"No session was opened and NOTHING was written — start again with a smaller first chunk",
			len([]byte(in.Content)), maxChunkBytes)
	}
	ws, err := d.resolveWorkspace(ctx)
	if err != nil {
		return nil, err
	}
	if err := cleanupExpiredChunkedWriteSessions(ws); err != nil {
		return nil, err
	}
	abs, err := resolveWritableFile(ws, in.Path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir parent: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), "."+filepath.Base(abs)+".write-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	tempPath := tmp.Name()
	if in.Content != "" {
		if _, err := tmp.WriteString(in.Content); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tempPath)
			return nil, fmt.Errorf("write initial chunk: %w", err)
		}
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return nil, fmt.Errorf("close temp file: %w", err)
	}
	sid, err := randomSessionID()
	if err != nil {
		_ = os.Remove(tempPath)
		return nil, err
	}
	_, statErr := os.Stat(abs)
	session := chunkedWriteSession{
		Path:          abs,
		TempPath:      tempPath,
		ExistedBefore: statErr == nil,
		UpdatedAt:     time.Now().UTC(),
		BytesWritten:  int64(len([]byte(in.Content))),
		ChunkCount:    boolToInt(in.Content != ""),
	}
	if err := saveChunkedWriteSession(ws, sid, session); err != nil {
		_ = os.Remove(tempPath)
		return nil, err
	}
	return &ChunkedWriteOutput{
		Mode:          "start",
		SessionID:     sid,
		Path:          abs,
		SizeBytes:     session.BytesWritten,
		ChunkCount:    session.ChunkCount,
		ExistedBefore: session.ExistedBefore,
		Message:       "write session started; append chunks in order, then call finish",
	}, nil
}

func chunkedWriteAppend(ctx context.Context, d *fsDeps, in *ChunkedWriteInput) (*ChunkedWriteOutput, error) {
	if strings.TrimSpace(in.SessionID) == "" {
		return nil, fmt.Errorf("session_id is required when mode=append")
	}
	if in.Content == "" {
		return nil, fmt.Errorf("content cannot be empty when mode=append")
	}
	if len([]byte(in.Content)) > maxChunkBytes {
		return nil, fmt.Errorf("content chunk too large: %d bytes (max %d). "+
			"NOTHING was written — split this chunk and re-send all of it before moving on",
			len([]byte(in.Content)), maxChunkBytes)
	}
	ws, err := d.resolveWorkspace(ctx)
	if err != nil {
		return nil, err
	}
	session, err := loadChunkedWriteSession(ws, in.SessionID)
	if err != nil {
		return nil, err
	}
	if err := validateSessionPaths(ws, session); err != nil {
		return nil, err
	}
	nextSize := session.BytesWritten + int64(len([]byte(in.Content)))
	if nextSize > maxChunkedWriteBytes {
		return nil, fmt.Errorf("chunked file too large: %d bytes (max %d). NOTHING was written",
			nextSize, maxChunkedWriteBytes)
	}
	f, err := os.OpenFile(session.TempPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open temp file: %w", err)
	}
	if _, err := f.WriteString(in.Content); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("append chunk: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close temp file: %w", err)
	}
	session.BytesWritten = nextSize
	session.ChunkCount++
	session.UpdatedAt = time.Now().UTC()
	if err := saveChunkedWriteSession(ws, in.SessionID, session); err != nil {
		return nil, err
	}
	return &ChunkedWriteOutput{
		Mode:       "append",
		SessionID:  in.SessionID,
		Path:       session.Path,
		SizeBytes:  session.BytesWritten,
		ChunkCount: session.ChunkCount,
		Message:    "chunk appended; append more chunks or call finish",
	}, nil
}

func chunkedWriteFinish(ctx context.Context, d *fsDeps, in *ChunkedWriteInput) (*ChunkedWriteOutput, error) {
	if strings.TrimSpace(in.SessionID) == "" {
		return nil, fmt.Errorf("session_id is required when mode=finish")
	}
	ws, err := d.resolveWorkspace(ctx)
	if err != nil {
		return nil, err
	}
	session, err := loadChunkedWriteSession(ws, in.SessionID)
	if err != nil {
		return nil, err
	}
	if err := validateSessionPaths(ws, session); err != nil {
		return nil, err
	}
	if err := os.Rename(session.TempPath, session.Path); err != nil {
		return nil, fmt.Errorf("finalize write: %w", err)
	}
	_ = deleteChunkedWriteSession(ws, in.SessionID)
	return &ChunkedWriteOutput{
		Mode:          "finish",
		SessionID:     in.SessionID,
		Path:          session.Path,
		SizeBytes:     session.BytesWritten,
		ChunkCount:    session.ChunkCount,
		ExistedBefore: session.ExistedBefore,
		Message:       "file saved",
	}, nil
}

func chunkedWriteAbort(ctx context.Context, d *fsDeps, in *ChunkedWriteInput) (*ChunkedWriteOutput, error) {
	if strings.TrimSpace(in.SessionID) == "" {
		return nil, fmt.Errorf("session_id is required when mode=abort")
	}
	ws, err := d.resolveWorkspace(ctx)
	if err != nil {
		return nil, err
	}
	session, err := loadChunkedWriteSession(ws, in.SessionID)
	if err != nil {
		return nil, err
	}
	_ = os.Remove(session.TempPath)
	_ = deleteChunkedWriteSession(ws, in.SessionID)
	return &ChunkedWriteOutput{
		Mode:       "abort",
		SessionID:  in.SessionID,
		Path:       session.Path,
		SizeBytes:  session.BytesWritten,
		ChunkCount: session.ChunkCount,
		Message:    "write session aborted and temp file removed",
	}, nil
}

func resolveWritableFile(workspaceRoot, userPath string) (string, error) {
	abs, err := scope.Resolve(workspaceRoot, userPath)
	if err != nil {
		return "", err
	}
	if abs == strings.TrimSuffix(workspaceRoot, string(filepath.Separator)) {
		return "", fmt.Errorf("refusing to write to the workspace root")
	}
	return abs, nil
}

func chunkedSessionDir(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, chunkedWriteSessionDir)
}

func chunkedSessionFile(workspaceRoot, sessionID string) (string, error) {
	if sessionID == "" || strings.ContainsAny(sessionID, `/\\`) {
		return "", fmt.Errorf("invalid session_id")
	}
	return filepath.Join(chunkedSessionDir(workspaceRoot), sessionID+".json"), nil
}

func saveChunkedWriteSession(workspaceRoot, sessionID string, session chunkedWriteSession) error {
	dir := chunkedSessionDir(workspaceRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir session dir: %w", err)
	}
	path, err := chunkedSessionFile(workspaceRoot, sessionID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write session: %w", err)
	}
	return nil
}

func loadChunkedWriteSession(workspaceRoot, sessionID string) (chunkedWriteSession, error) {
	path, err := chunkedSessionFile(workspaceRoot, sessionID)
	if err != nil {
		return chunkedWriteSession{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return chunkedWriteSession{}, fmt.Errorf("unknown write session: %s", sessionID)
		}
		return chunkedWriteSession{}, fmt.Errorf("read session: %w", err)
	}
	var session chunkedWriteSession
	if err := json.Unmarshal(raw, &session); err != nil {
		return chunkedWriteSession{}, fmt.Errorf("invalid session state: %w", err)
	}
	if time.Since(session.UpdatedAt) > chunkedWriteTTL {
		_ = os.Remove(session.TempPath)
		_ = deleteChunkedWriteSession(workspaceRoot, sessionID)
		return chunkedWriteSession{}, fmt.Errorf("write session expired: %s", sessionID)
	}
	return session, nil
}

func deleteChunkedWriteSession(workspaceRoot, sessionID string) error {
	path, err := chunkedSessionFile(workspaceRoot, sessionID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func validateSessionPaths(workspaceRoot string, session chunkedWriteSession) error {
	root := strings.TrimSuffix(workspaceRoot, string(filepath.Separator)) + string(filepath.Separator)
	if !strings.HasPrefix(session.Path, root) || !strings.HasPrefix(session.TempPath, root) {
		return fmt.Errorf("session path escaped workspace")
	}
	return nil
}

func cleanupExpiredChunkedWriteSessions(workspaceRoot string) error {
	dir := chunkedSessionDir(workspaceRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read session dir: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var session chunkedWriteSession
		if err := json.Unmarshal(raw, &session); err != nil {
			continue
		}
		if time.Since(session.UpdatedAt) > chunkedWriteTTL {
			_ = os.Remove(session.TempPath)
			_ = os.Remove(path)
		}
	}
	return nil
}

func randomSessionID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
