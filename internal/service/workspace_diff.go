package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/guyi-a/Interview-Agent/internal/agent/scope"
	"github.com/guyi-a/Interview-Agent/internal/approval"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/workspacegit"
)

const (
	maxChangedFiles    = 500
	maxDiffFileBytes   = 2 * 1024 * 1024
	maxDiffOutputBytes = 1024 * 1024
)

var (
	ErrInvalidChangeScope = errors.New("invalid change scope")
	ErrChangeNotFound     = errors.New("workspace change not found")
)

type WorkspaceDiffService struct {
	workspace *WorkspaceService
	messages  *repository.MessageRepo
	changes   *repository.WorkspaceChangeRepo
}

func NewWorkspaceDiffService(
	workspace *WorkspaceService,
	messages *repository.MessageRepo,
	changes *repository.WorkspaceChangeRepo,
) *WorkspaceDiffService {
	return &WorkspaceDiffService{workspace: workspace, messages: messages, changes: changes}
}

type WorkspaceChangedFile struct {
	Path        string   `json:"path"`
	OldPath     string   `json:"old_path,omitempty"`
	Status      string   `json:"status"`
	Additions   int      `json:"additions"`
	Deletions   int      `json:"deletions"`
	Binary      bool     `json:"binary,omitempty"`
	Sensitive   bool     `json:"sensitive,omitempty"`
	TooLarge    bool     `json:"too_large,omitempty"`
	Attribution string   `json:"attribution,omitempty"`
	Tools       []string `json:"tools,omitempty"`
}

type WorkspaceChangesResult struct {
	Workspace      WorkspaceMeta          `json:"workspace"`
	Scope          string                 `json:"scope"`
	GitRepository  bool                   `json:"git_repository"`
	UserMessageSeq int                    `json:"user_message_seq,omitempty"`
	Files          []WorkspaceChangedFile `json:"files"`
	Truncated      bool                   `json:"truncated,omitempty"`
}

type WorkspaceDiffResult struct {
	WorkspaceChangedFile
	Scope          string `json:"scope"`
	UserMessageSeq int    `json:"user_message_seq,omitempty"`
	Patch          string `json:"patch,omitempty"`
	Truncated      bool   `json:"truncated,omitempty"`
}

func (s *WorkspaceDiffService) Changes(
	ctx context.Context,
	conversationID, projectID, changeScope string,
) (*WorkspaceChangesResult, error) {
	if changeScope == "" {
		changeScope = "agent"
	}
	root, resolvedProjectID, err := s.workspace.Root(ctx, conversationID, projectID)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	meta := WorkspaceMeta{ProjectID: resolvedProjectID, RootName: filepath.Base(root)}
	switch changeScope {
	case "all":
		files, gitRepo, truncated, err := s.gitChanges(ctx, root)
		if err != nil {
			return nil, err
		}
		return &WorkspaceChangesResult{
			Workspace: meta, Scope: "all", GitRepository: gitRepo,
			Files: files, Truncated: truncated,
		}, nil
	case "agent":
		userSeq, files, truncated, err := s.agentChanges(ctx, conversationID, root)
		if err != nil {
			return nil, err
		}
		_, gitErr := workspacegit.RepositoryRoot(ctx, root)
		return &WorkspaceChangesResult{
			Workspace: meta, Scope: "agent",
			GitRepository: gitErr == nil, UserMessageSeq: userSeq,
			Files: files, Truncated: truncated,
		}, nil
	default:
		return nil, ErrInvalidChangeScope
	}
}

func (s *WorkspaceDiffService) Diff(
	ctx context.Context,
	conversationID, projectID, changeScope, userPath string,
) (*WorkspaceDiffResult, error) {
	if strings.TrimSpace(userPath) == "" {
		return nil, fmt.Errorf("path is required")
	}
	root, _, err := s.workspace.Root(ctx, conversationID, projectID)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	abs, err := scope.Resolve(root, userPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPathOutsideWorkspace, err)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." {
		return nil, ErrChangeNotFound
	}
	rel = filepath.ToSlash(rel)
	if changeScope == "" {
		changeScope = "agent"
	}
	switch changeScope {
	case "all":
		return s.gitFileDiff(ctx, conversationID, root, rel)
	case "agent":
		return s.agentFileDiff(ctx, conversationID, root, rel)
	default:
		return nil, ErrInvalidChangeScope
	}
}

func (s *WorkspaceDiffService) gitChanges(
	ctx context.Context,
	root string,
) ([]WorkspaceChangedFile, bool, bool, error) {
	status, err := workspacegit.Status(ctx, root)
	if errors.Is(err, workspacegit.ErrNotRepository) ||
		errors.Is(err, workspacegit.ErrNotRepositoryRoot) {
		return []WorkspaceChangedFile{}, false, false, nil
	}
	if err != nil {
		return nil, false, false, err
	}
	stats, err := workspacegit.NumStat(ctx, root)
	if err != nil {
		return nil, true, false, err
	}
	statByPath := make(map[string]workspacegit.NumStatEntry, len(stats))
	for _, stat := range stats {
		statByPath[stat.Path] = stat
	}
	files := make([]WorkspaceChangedFile, 0, min(len(status), maxChangedFiles))
	truncated := false
	for _, entry := range status {
		if len(files) >= maxChangedFiles {
			truncated = true
			break
		}
		file := WorkspaceChangedFile{
			Path: entry.Path, OldPath: entry.OldPath,
			Status: statusName(entry), Attribution: "all",
		}
		if stat, ok := statByPath[entry.Path]; ok {
			file.Additions = stat.Additions
			file.Deletions = stat.Deletions
			file.Binary = stat.Binary
			if file.OldPath == "" {
				file.OldPath = stat.OldPath
			}
		} else if file.Status == "added" {
			meta := currentFileMeta(filepath.Join(root, filepath.FromSlash(file.Path)))
			file.Additions = meta.lines
			file.Binary = meta.binary
			file.TooLarge = meta.tooLarge
		}
		_, file.Sensitive = approval.PathIsSensitive(file.Path)
		if file.Sensitive {
			file.Additions, file.Deletions = 0, 0
		}
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, true, truncated, nil
}

func (s *WorkspaceDiffService) agentChanges(
	ctx context.Context,
	conversationID, root string,
) (int, []WorkspaceChangedFile, bool, error) {
	userSeq, err := s.messages.LatestUserSeq(ctx, conversationID)
	if err != nil || userSeq == 0 {
		return userSeq, []WorkspaceChangedFile{}, false, err
	}
	baselines, err := s.changes.ListBaselines(ctx, conversationID, userSeq)
	if err != nil {
		return userSeq, nil, false, err
	}
	events, err := s.changes.ListEvents(ctx, conversationID, userSeq)
	if err != nil {
		return userSeq, nil, false, err
	}
	toolsByPath := eventTools(events)
	files := make([]WorkspaceChangedFile, 0, min(len(baselines), maxChangedFiles))
	truncated := false
	for _, baseline := range baselines {
		if len(files) >= maxChangedFiles {
			truncated = true
			break
		}
		current := currentFileMeta(filepath.Join(root, filepath.FromSlash(baseline.Path)))
		if current.directory {
			continue
		}
		if !baselineChanged(baseline, current) {
			continue
		}
		file := WorkspaceChangedFile{
			Path: baseline.Path, Status: baselineStatus(baseline.Existed, current.existed),
			Binary:    baseline.Binary || current.binary,
			Sensitive: baseline.Sensitive, TooLarge: baseline.TooLarge || current.tooLarge,
			Attribution: "agent", Tools: toolsByPath[baseline.Path],
		}
		if !file.Binary && !file.Sensitive && !file.TooLarge {
			patch, diffErr := workspacegit.NoIndexDiff(
				ctx, baseline.Path, baseline.Content, current.content,
				baseline.Existed, current.existed,
			)
			if diffErr == nil {
				file.Additions, file.Deletions = countPatchLines(string(patch))
			}
		}
		files = append(files, file)
	}
	return userSeq, files, truncated, nil
}

func (s *WorkspaceDiffService) gitFileDiff(
	ctx context.Context,
	conversationID, root, rel string,
) (*WorkspaceDiffResult, error) {
	files, gitRepo, _, err := s.gitChanges(ctx, root)
	if err != nil {
		return nil, err
	}
	if !gitRepo {
		return nil, workspacegit.ErrNotRepository
	}
	file := changedFileByPath(files, rel)
	if file == nil {
		return nil, ErrChangeNotFound
	}
	out := &WorkspaceDiffResult{WorkspaceChangedFile: *file, Scope: "all"}
	if file.Binary || file.Sensitive || file.TooLarge {
		return out, nil
	}
	current := currentFileMeta(filepath.Join(root, filepath.FromSlash(rel)))
	if current.directory {
		return nil, ErrChangeNotFound
	}
	var patch []byte
	if file.Status == "added" {
		patch, err = workspacegit.NoIndexDiff(ctx, rel, nil, current.content, false, true)
	} else {
		patch, err = workspacegit.Diff(ctx, root, rel)
	}
	if err != nil {
		return nil, err
	}
	out.Patch, out.Truncated = truncateDiff(patch)
	return out, nil
}

func (s *WorkspaceDiffService) agentFileDiff(
	ctx context.Context,
	conversationID, root, rel string,
) (*WorkspaceDiffResult, error) {
	userSeq, err := s.messages.LatestUserSeq(ctx, conversationID)
	if err != nil || userSeq == 0 {
		return nil, ErrChangeNotFound
	}
	baseline, err := s.changes.GetBaseline(ctx, conversationID, userSeq, rel)
	if err != nil {
		return nil, err
	}
	if baseline == nil {
		return nil, ErrChangeNotFound
	}
	current := currentFileMeta(filepath.Join(root, filepath.FromSlash(rel)))
	if !baselineChanged(*baseline, current) {
		return nil, ErrChangeNotFound
	}
	events, _ := s.changes.ListEvents(ctx, conversationID, userSeq)
	file := WorkspaceChangedFile{
		Path: rel, Status: baselineStatus(baseline.Existed, current.existed),
		Binary:    baseline.Binary || current.binary,
		Sensitive: baseline.Sensitive, TooLarge: baseline.TooLarge || current.tooLarge,
		Attribution: "agent", Tools: eventTools(events)[rel],
	}
	out := &WorkspaceDiffResult{
		WorkspaceChangedFile: file, Scope: "agent", UserMessageSeq: userSeq,
	}
	if file.Binary || file.Sensitive || file.TooLarge {
		return out, nil
	}
	patch, err := workspacegit.NoIndexDiff(
		ctx, rel, baseline.Content, current.content,
		baseline.Existed, current.existed,
	)
	if err != nil {
		return nil, err
	}
	out.Additions, out.Deletions = countPatchLines(string(patch))
	out.Patch, out.Truncated = truncateDiff(patch)
	return out, nil
}

type fileMeta struct {
	existed   bool
	content   []byte
	sha256    string
	binary    bool
	tooLarge  bool
	directory bool
	size      int64
	modTime   int64
	lines     int
}

func currentFileMeta(path string) fileMeta {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileMeta{}
	}
	if err != nil || !info.Mode().IsRegular() {
		return fileMeta{existed: true, tooLarge: true, directory: info != nil && info.IsDir()}
	}
	out := fileMeta{
		existed: true,
		size:    info.Size(),
		modTime: info.ModTime().UnixNano(),
	}
	if info.Size() > maxDiffFileBytes {
		out.tooLarge = true
		return out
	}
	data, err := os.ReadFile(path)
	if err != nil {
		out.tooLarge = true
		return out
	}
	out.content = data
	sum := sha256.Sum256(data)
	out.sha256 = hex.EncodeToString(sum[:])
	out.binary = strings.IndexByte(string(data[:min(len(data), 8192)]), 0) >= 0 || !utf8.Valid(data)
	if !out.binary && len(data) > 0 {
		out.lines = strings.Count(string(data), "\n")
		if data[len(data)-1] != '\n' {
			out.lines++
		}
	}
	return out
}

func baselineChanged(baseline model.WorkspaceFileBaseline, current fileMeta) bool {
	if baseline.Existed != current.existed {
		return true
	}
	if !baseline.Existed {
		return false
	}
	if baseline.Sensitive || baseline.TooLarge || current.tooLarge {
		return baseline.Size != current.size || baseline.ModTime != current.modTime
	}
	return baseline.SHA256 != current.sha256
}

func baselineStatus(before, after bool) string {
	switch {
	case !before && after:
		return "added"
	case before && !after:
		return "deleted"
	default:
		return "modified"
	}
}

func statusName(entry workspacegit.StatusEntry) string {
	if entry.IndexStatus == '?' && entry.WorkStatus == '?' {
		return "added"
	}
	if entry.IndexStatus == 'R' || entry.WorkStatus == 'R' {
		return "renamed"
	}
	if entry.IndexStatus == 'D' || entry.WorkStatus == 'D' {
		return "deleted"
	}
	if entry.IndexStatus == 'A' || entry.WorkStatus == 'A' {
		return "added"
	}
	return "modified"
}

func eventTools(events []model.WorkspaceChangeEvent) map[string][]string {
	out := make(map[string][]string)
	seen := make(map[string]map[string]struct{})
	for _, event := range events {
		if event.Path == "" {
			continue
		}
		if seen[event.Path] == nil {
			seen[event.Path] = make(map[string]struct{})
		}
		if _, ok := seen[event.Path][event.ToolName]; ok {
			continue
		}
		seen[event.Path][event.ToolName] = struct{}{}
		out[event.Path] = append(out[event.Path], event.ToolName)
	}
	return out
}

func countPatchLines(patch string) (additions, deletions int) {
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		if strings.HasPrefix(line, "+") {
			additions++
		} else if strings.HasPrefix(line, "-") {
			deletions++
		}
	}
	return
}

func changedFileByPath(files []WorkspaceChangedFile, path string) *WorkspaceChangedFile {
	for i := range files {
		if files[i].Path == path || files[i].OldPath == path {
			return &files[i]
		}
	}
	return nil
}

func truncateDiff(raw []byte) (string, bool) {
	if len(raw) <= maxDiffOutputBytes {
		return string(raw), false
	}
	return string(raw[:maxDiffOutputBytes]), true
}
