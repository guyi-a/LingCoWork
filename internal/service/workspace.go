package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/guyi-a/Interview-Agent/internal/agent/scope"
	"github.com/guyi-a/Interview-Agent/internal/memory"
	"github.com/guyi-a/Interview-Agent/internal/repository"
)

var (
	ErrNoWorkspace          = errors.New("no workspace mounted for this conversation")
	ErrPathOutsideWorkspace = errors.New("path outside workspace")
	ErrFileNotFound         = errors.New("file not found")
	ErrPathIsDirectory      = errors.New("path is a directory")
)

const (
	maxTreeEntries = 500
	maxFileBytes   = 512 * 1024
	binarySniffLen = 512
)

type WorkspaceService struct {
	convRepo    *repository.ConversationRepo
	projectRepo *repository.ProjectRepo
}

func NewWorkspaceService(convRepo *repository.ConversationRepo, projectRepo *repository.ProjectRepo) *WorkspaceService {
	return &WorkspaceService{convRepo: convRepo, projectRepo: projectRepo}
}

type WorkspaceMeta struct {
	ProjectID string `json:"project_id"`
	RootName  string `json:"root_name"`
}

type TreeEntry struct {
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	IsDir      bool      `json:"is_dir"`
	Size       int64     `json:"size,omitempty"`
	ModifiedAt time.Time `json:"modified_at"`
}

type TreeResult struct {
	Workspace WorkspaceMeta `json:"workspace"`
	Entries   []TreeEntry   `json:"entries"`
	Truncated bool          `json:"truncated,omitempty"`
}

type FileKind string

const (
	KindMarkdown    FileKind = "markdown"
	KindText        FileKind = "text"
	KindImage       FileKind = "image"
	KindBinary      FileKind = "binary"
	KindUnsupported FileKind = "unsupported"
)

type FileResult struct {
	Path      string   `json:"path"`
	Name      string   `json:"name"`
	Size      int64    `json:"size"`
	Mime      string   `json:"mime,omitempty"`
	Kind      FileKind `json:"kind"`
	IsBinary  bool     `json:"is_binary"`
	Content   string   `json:"content,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

func (s *WorkspaceService) resolveWorkspace(ctx context.Context, convID string) (root string, projectID string, err error) {
	if convID == "" {
		return "", "", ErrNoWorkspace
	}
	conv, err := s.convRepo.Get(ctx, convID)
	if err != nil {
		return "", "", fmt.Errorf("load conversation: %w", err)
	}
	if conv == nil || conv.ProjectID == nil || *conv.ProjectID == "" {
		return "", "", ErrNoWorkspace
	}
	return s.resolveProjectWorkspace(ctx, *conv.ProjectID)
}

func (s *WorkspaceService) resolveProjectWorkspace(ctx context.Context, projectID string) (root string, resolvedProjectID string, err error) {
	if projectID == "" {
		return "", "", ErrNoWorkspace
	}
	project, err := s.projectRepo.Get(ctx, projectID)
	if err != nil {
		return "", "", fmt.Errorf("load project: %w", err)
	}
	if project == nil || project.Workspace == "" {
		return "", "", ErrNoWorkspace
	}
	return project.Workspace, project.ID, nil
}

func (s *WorkspaceService) resolveWorkspaceFor(ctx context.Context, convID, projectID string) (root string, resolvedProjectID string, err error) {
	if projectID != "" {
		return s.resolveProjectWorkspace(ctx, projectID)
	}
	return s.resolveWorkspace(ctx, convID)
}

// Root exposes the workspace root to callers outside this package that need to
// locate a file sitting at a known path inside it — project-level memory being
// the one today. Exported so that resolution stays defined once here instead of
// being reimplemented next to every such file.
func (s *WorkspaceService) Root(ctx context.Context, convID, projectID string) (string, string, error) {
	return s.resolveWorkspaceFor(ctx, convID, projectID)
}

func (s *WorkspaceService) resolvePath(ctx context.Context, convID, projectID, userPath string) (root, resolvedProjectID, abs string, err error) {
	root, resolvedProjectID, err = s.resolveWorkspaceFor(ctx, convID, projectID)
	if err != nil {
		return
	}
	abs, err = scope.Resolve(root, userPath)
	if err != nil {
		err = fmt.Errorf("%w: %v", ErrPathOutsideWorkspace, err)
		return
	}
	return
}

func (s *WorkspaceService) Tree(ctx context.Context, convID string) (*TreeResult, error) {
	return s.TreeFor(ctx, convID, "")
}

func (s *WorkspaceService) TreeFor(ctx context.Context, convID, projectID string) (*TreeResult, error) {
	root, resolvedProjectID, err := s.resolveWorkspaceFor(ctx, convID, projectID)
	if err != nil {
		return nil, err
	}

	entries := make([]TreeEntry, 0, 64)
	truncated := false

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// A single unreadable entry shouldn't fail the whole tree.
			return nil
		}
		if path == root {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		// 项目记忆有自己的编辑入口，在文件树里再列一遍只会让人以为该从这里
		// 改它 —— 而从这里点进去只有只读预览。注意这个过滤只作用于给界面看的
		// 树：Agent 的 list_files 走另一条路，它必须还能看见这个文件，否则就
		// 读不到也写不了项目记忆。
		if !d.IsDir() && filepath.ToSlash(rel) == memory.FileName {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		entry := TreeEntry{
			Path:       filepath.ToSlash(rel),
			Name:       name,
			IsDir:      d.IsDir(),
			ModifiedAt: info.ModTime(),
		}
		if !d.IsDir() {
			entry.Size = info.Size()
		}
		entries = append(entries, entry)
		if len(entries) >= maxTreeEntries {
			truncated = true
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walk workspace: %w", walkErr)
	}

	return &TreeResult{
		Workspace: WorkspaceMeta{
			ProjectID: resolvedProjectID,
			RootName:  filepath.Base(root),
		},
		Entries:   entries,
		Truncated: truncated,
	}, nil
}

func (s *WorkspaceService) File(ctx context.Context, convID, userPath string) (*FileResult, error) {
	return s.FileFor(ctx, convID, "", userPath)
}

func (s *WorkspaceService) FileFor(ctx context.Context, convID, projectID, userPath string) (*FileResult, error) {
	_, _, abs, err := s.resolvePath(ctx, convID, projectID, userPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrFileNotFound
		}
		return nil, err
	}
	if info.IsDir() {
		return nil, ErrPathIsDirectory
	}

	ext := strings.ToLower(filepath.Ext(abs))
	result := &FileResult{
		Path: filepath.ToSlash(userPath),
		Name: filepath.Base(abs),
		Size: info.Size(),
		Mime: mime.TypeByExtension(ext),
		Kind: classifyExt(ext),
	}

	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sniff := make([]byte, binarySniffLen)
	n, _ := f.Read(sniff)
	sniff = sniff[:n]
	binary := isBinaryContent(sniff)

	if binary {
		result.IsBinary = true
		if result.Kind == KindText || result.Kind == KindMarkdown {
			result.Kind = KindBinary
		}
		return result, nil
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, maxFileBytes)
	n, err = io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	result.Content = string(buf[:n])
	if info.Size() > int64(n) {
		result.Truncated = true
	}
	return result, nil
}

func (s *WorkspaceService) OpenForDownload(ctx context.Context, convID, userPath string) (abs, name string, err error) {
	return s.OpenForDownloadFor(ctx, convID, "", userPath)
}

func (s *WorkspaceService) OpenForDownloadFor(ctx context.Context, convID, projectID, userPath string) (abs, name string, err error) {
	_, _, abs, err = s.resolvePath(ctx, convID, projectID, userPath)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", ErrFileNotFound
		}
		return "", "", err
	}
	if info.IsDir() {
		return "", "", ErrPathIsDirectory
	}
	return abs, filepath.Base(abs), nil
}

func classifyExt(ext string) FileKind {
	switch ext {
	case ".md", ".markdown":
		return KindMarkdown
	case ".txt", ".log", ".json", ".yaml", ".yml", ".csv",
		".go", ".py", ".js", ".ts", ".jsx", ".tsx", ".sh",
		".html", ".css", ".xml", ".toml", ".ini", ".env",
		".mod", ".sum":
		return KindText
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".bmp":
		return KindImage
	case "":
		return KindText
	default:
		return KindUnsupported
	}
}

func isBinaryContent(sniff []byte) bool {
	for _, b := range sniff {
		if b == 0 {
			return true
		}
	}
	return false
}
