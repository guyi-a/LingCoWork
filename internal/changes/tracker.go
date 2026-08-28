package changes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/eino/compose"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/approval"
	"github.com/guyi-a/Interview-Agent/internal/effect"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/workspacegit"
)

const (
	maxBaselineBytes = 2 * 1024 * 1024
	maxBaselineFiles = 2000
	binarySniffBytes = 8192
)

type Tracker struct {
	changes  *repository.WorkspaceChangeRepo
	messages *repository.MessageRepo
	convs    *repository.ConversationRepo
	projects *repository.ProjectRepo
	effects  *effect.Registry
}

type runContext struct {
	projectID      string
	conversationID string
	userSeq        int
	workspace      string
}

type fileFingerprint struct {
	existed bool
	size    int64
	mtime   int64
	sha256  string
}

func NewTracker(
	changes *repository.WorkspaceChangeRepo,
	messages *repository.MessageRepo,
	convs *repository.ConversationRepo,
	projects *repository.ProjectRepo,
	effects *effect.Registry,
) *Tracker {
	if changes == nil || messages == nil || convs == nil || projects == nil || effects == nil {
		return nil
	}
	return &Tracker{
		changes: changes, messages: messages, convs: convs,
		projects: projects, effects: effects,
	}
}

func (t *Tracker) Middleware() compose.ToolMiddleware {
	if t == nil {
		return compose.ToolMiddleware{}
	}
	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				run, ok := t.resolveRun(ctx)
				if !ok {
					return next(ctx, input)
				}
				e := t.effects.Derive(ctx, input.Name, input.Arguments)
				if input.Name == "run_command" && e.Kind == effect.KindProcessExec {
					return t.captureCommand(ctx, run, input, next)
				}
				paths := mutationPaths(input.Name, e)
				for _, path := range paths {
					t.capturePath(ctx, run, path)
				}
				out, err := next(ctx, input)
				if len(paths) > 0 {
					for _, path := range paths {
						t.recordEvent(ctx, run, input, path, err == nil)
					}
				}
				if err == nil && e.Kind == effect.KindFileTransfer &&
					e.DestScope == effect.ScopeWorkspace {
					t.captureAddedTree(ctx, run, input, e.DestPath)
				}
				return out, err
			}
		},
	}
}

func (t *Tracker) captureAddedTree(
	ctx context.Context,
	run runContext,
	input *compose.ToolInput,
	absPath string,
) {
	info, err := os.Stat(absPath)
	if err != nil || !info.IsDir() {
		return
	}
	count := 0
	_ = filepath.WalkDir(absPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == absPath || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		count++
		if count > maxBaselineFiles {
			return filepath.SkipAll
		}
		t.captureFile(ctx, run, path, &model.WorkspaceFileBaseline{Existed: false})
		t.recordEvent(ctx, run, input, path, true)
		return nil
	})
}

func (t *Tracker) resolveRun(ctx context.Context) (runContext, bool) {
	conversationID := contextkey.ConversationID(ctx)
	if conversationID == "" {
		return runContext{}, false
	}
	conv, err := t.convs.Get(ctx, conversationID)
	if err != nil || conv == nil || conv.ProjectID == nil || *conv.ProjectID == "" {
		return runContext{}, false
	}
	project, err := t.projects.Get(ctx, *conv.ProjectID)
	if err != nil || project == nil || project.Workspace == "" {
		return runContext{}, false
	}
	workspace, err := filepath.EvalSymlinks(filepath.Clean(project.Workspace))
	if err != nil {
		return runContext{}, false
	}
	userSeq, err := t.messages.LatestUserSeq(ctx, conversationID)
	if err != nil || userSeq == 0 {
		return runContext{}, false
	}
	return runContext{
		projectID: project.ID, conversationID: conversationID,
		userSeq: userSeq, workspace: workspace,
	}, true
}

func mutationPaths(toolName string, e effect.Effect) []string {
	add := func(paths []string, path string, scope effect.Scope) []string {
		if path != "" && scope == effect.ScopeWorkspace {
			return append(paths, path)
		}
		return paths
	}
	var paths []string
	switch e.Kind {
	case effect.KindFileWrite:
		paths = add(paths, e.Path, e.Scope)
	case effect.KindFileTransfer:
		if toolName == "mv" {
			paths = add(paths, e.Path, e.PathScope)
		}
		paths = add(paths, e.DestPath, e.DestScope)
	}
	return uniqueStrings(paths)
}

func (t *Tracker) capturePath(ctx context.Context, run runContext, absPath string) {
	info, err := os.Lstat(absPath)
	if err == nil && info.IsDir() {
		count := 0
		_ = filepath.WalkDir(absPath, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if path == absPath {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			count++
			if count > maxBaselineFiles {
				return filepath.SkipAll
			}
			t.captureFile(ctx, run, path, nil)
			return nil
		})
		return
	}
	t.captureFile(ctx, run, absPath, nil)
}

func (t *Tracker) captureFile(
	ctx context.Context,
	run runContext,
	absPath string,
	override *model.WorkspaceFileBaseline,
) {
	rel, ok := relativeWorkspacePath(run.workspace, absPath)
	if !ok {
		return
	}
	var baseline model.WorkspaceFileBaseline
	if override != nil {
		baseline = *override
	} else {
		if _, sensitive := approval.PathIsSensitive(rel); sensitive {
			baseline = snapshotSensitiveFile(absPath)
		} else {
			baseline = snapshotFile(absPath)
		}
	}
	if _, sensitive := approval.PathIsSensitive(rel); sensitive {
		baseline.Sensitive = true
		baseline.Content = nil
	}
	baseline.ProjectID = run.projectID
	baseline.ConversationID = run.conversationID
	baseline.UserMessageSeq = run.userSeq
	baseline.Path = rel
	_, _ = t.changes.CreateBaselineIfAbsent(ctx, &baseline)
}

func snapshotSensitiveFile(absPath string) model.WorkspaceFileBaseline {
	info, err := os.Lstat(absPath)
	if errors.Is(err, os.ErrNotExist) {
		return model.WorkspaceFileBaseline{Existed: false, Sensitive: true}
	}
	if err != nil || !info.Mode().IsRegular() {
		return model.WorkspaceFileBaseline{Existed: false, Sensitive: true}
	}
	return model.WorkspaceFileBaseline{
		Existed:   true,
		Size:      info.Size(),
		ModTime:   info.ModTime().UnixNano(),
		Mode:      uint32(info.Mode().Perm()),
		Sensitive: true,
	}
}

func snapshotFile(absPath string) model.WorkspaceFileBaseline {
	info, err := os.Lstat(absPath)
	if errors.Is(err, os.ErrNotExist) {
		return model.WorkspaceFileBaseline{Existed: false}
	}
	if err != nil || !info.Mode().IsRegular() {
		return model.WorkspaceFileBaseline{Existed: false}
	}
	out := model.WorkspaceFileBaseline{
		Existed: true,
		Size:    info.Size(),
		ModTime: info.ModTime().UnixNano(),
		Mode:    uint32(info.Mode().Perm()),
	}
	if info.Size() > maxBaselineBytes {
		out.TooLarge = true
		return out
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		out.TooLarge = true
		return out
	}
	sum := sha256.Sum256(data)
	out.SHA256 = hex.EncodeToString(sum[:])
	if len(data) > 0 {
		sniff := data[:min(len(data), binarySniffBytes)]
		out.Binary = strings.IndexByte(string(sniff), 0) >= 0 || !utf8.Valid(data)
	}
	if !out.Binary {
		out.Content = data
	}
	return out
}

func (t *Tracker) captureCommand(
	ctx context.Context,
	run runContext,
	input *compose.ToolInput,
	next compose.InvokableToolEndpoint,
) (*compose.ToolOutput, error) {
	before, statusErr := workspacegit.Status(ctx, run.workspace)
	beforePaths := statusPaths(before)
	beforeFingerprints := make(map[string]fileFingerprint, len(beforePaths))
	beforeBaselines := make(map[string]model.WorkspaceFileBaseline, len(beforePaths))
	for path := range beforePaths {
		abs := filepath.Join(run.workspace, filepath.FromSlash(path))
		beforeFingerprints[path] = fingerprintFile(abs)
		if _, sensitive := approval.PathIsSensitive(path); sensitive {
			beforeBaselines[path] = snapshotSensitiveFile(abs)
		} else {
			beforeBaselines[path] = snapshotFile(abs)
		}
	}

	out, callErr := next(ctx, input)

	after, afterErr := workspacegit.Status(ctx, run.workspace)
	if statusErr != nil || afterErr != nil {
		return out, callErr
	}
	union := make(map[string]struct{}, len(beforePaths)+len(after))
	for path := range beforePaths {
		union[path] = struct{}{}
	}
	for path := range statusPaths(after) {
		union[path] = struct{}{}
	}
	for path := range union {
		abs := filepath.Join(run.workspace, filepath.FromSlash(path))
		var baseline model.WorkspaceFileBaseline
		if _, existedBefore := beforePaths[path]; existedBefore {
			baseline = beforeBaselines[path]
		} else {
			if data, existsAtHead, err := workspacegit.HeadFile(ctx, run.workspace, path); err == nil && existsAtHead {
				baseline = snapshotBytes(data)
				baseline.Existed = true
				beforeFingerprints[path] = fingerprintBytes(data)
			} else {
				baseline = model.WorkspaceFileBaseline{Existed: false}
				beforeFingerprints[path] = fileFingerprint{}
			}
		}
		if beforeFingerprints[path] == fingerprintFile(abs) {
			continue
		}
		t.captureFile(ctx, run, abs, &baseline)
		t.recordEvent(ctx, run, input, abs, callErr == nil)
	}
	return out, callErr
}

func fingerprintFile(path string) fileFingerprint {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileFingerprint{}
	}
	if err != nil {
		return fileFingerprint{existed: true}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fileFingerprint{existed: true}
	}
	if info.Size() > maxBaselineBytes {
		return fileFingerprint{
			existed: true,
			size:    info.Size(),
			mtime:   info.ModTime().UnixNano(),
		}
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fileFingerprint{existed: true, size: info.Size()}
	}
	return fileFingerprint{
		existed: true,
		size:    info.Size(),
		mtime:   info.ModTime().UnixNano(),
		sha256:  hex.EncodeToString(hash.Sum(nil)),
	}
}

func fingerprintBytes(data []byte) fileFingerprint {
	sum := sha256.Sum256(data)
	return fileFingerprint{
		existed: true,
		size:    int64(len(data)),
		sha256:  hex.EncodeToString(sum[:]),
	}
}

func snapshotBytes(data []byte) model.WorkspaceFileBaseline {
	out := model.WorkspaceFileBaseline{Size: int64(len(data))}
	if len(data) > maxBaselineBytes {
		out.TooLarge = true
		return out
	}
	sum := sha256.Sum256(data)
	out.SHA256 = hex.EncodeToString(sum[:])
	if len(data) > 0 {
		sniff := data[:min(len(data), binarySniffBytes)]
		out.Binary = strings.IndexByte(string(sniff), 0) >= 0 || !utf8.Valid(data)
	}
	if !out.Binary {
		out.Content = append([]byte(nil), data...)
	}
	return out
}

func (t *Tracker) recordEvent(
	ctx context.Context,
	run runContext,
	input *compose.ToolInput,
	absPath string,
	succeeded bool,
) {
	rel, ok := relativeWorkspacePath(run.workspace, absPath)
	if !ok {
		return
	}
	_ = t.changes.CreateEvent(ctx, &model.WorkspaceChangeEvent{
		ProjectID: run.projectID, ConversationID: run.conversationID,
		UserMessageSeq: run.userSeq, ToolCallID: input.CallID,
		ToolName: input.Name, Operation: operationForTool(input.Name),
		Path: rel, Attribution: "agent", Succeeded: succeeded,
	})
}

func operationForTool(name string) string {
	switch name {
	case "apply_patch":
		return "patch"
	case "rm":
		return "delete"
	case "mv":
		return "rename"
	case "cp":
		return "copy"
	case "run_command":
		return "shell"
	default:
		return "write"
	}
}

func statusPaths(entries []workspacegit.StatusEntry) map[string]struct{} {
	out := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.Path != "" {
			out[entry.Path] = struct{}{}
		}
		if entry.OldPath != "" {
			out[entry.OldPath] = struct{}{}
		}
	}
	return out
}

func relativeWorkspacePath(root, absPath string) (string, bool) {
	if canonical, err := filepath.EvalSymlinks(filepath.Clean(absPath)); err == nil {
		absPath = canonical
	} else if parent, parentErr := filepath.EvalSymlinks(filepath.Dir(filepath.Clean(absPath))); parentErr == nil {
		absPath = filepath.Join(parent, filepath.Base(absPath))
	}
	rel, err := filepath.Rel(root, absPath)
	if err != nil || rel == "." || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
