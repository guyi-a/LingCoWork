package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/guyi-a/Interview-Agent/internal/agent/browseruse"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

var ErrInvalidWorkspace = errors.New("invalid workspace")

type ProjectService struct {
	repo       *repository.ProjectRepo
	convRepo   *repository.ConversationRepo
	manager    *stream.Manager
	browserMgr *browseruse.Manager

	openMu sync.Mutex
}

func NewProjectService(
	repo *repository.ProjectRepo,
	convRepo *repository.ConversationRepo,
	manager *stream.Manager,
	browserMgr *browseruse.Manager,
) *ProjectService {
	return &ProjectService{
		repo:       repo,
		convRepo:   convRepo,
		manager:    manager,
		browserMgr: browserMgr,
	}
}

func (s *ProjectService) List(ctx context.Context) ([]model.Project, error) {
	return s.repo.List(ctx)
}

func (s *ProjectService) Get(ctx context.Context, id string) (*model.Project, error) {
	return s.repo.Get(ctx, id)
}

func (s *ProjectService) Rename(ctx context.Context, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name is empty")
	}
	return s.repo.UpdateName(ctx, id, name)
}

// OpenOrCreateFromPath registers a user-selected directory as a project.
// The path stored in the database is canonical so reopening the same folder
// reuses the existing project.
func (s *ProjectService) OpenOrCreateFromPath(
	ctx context.Context,
	rawPath string,
	name string,
) (*model.Project, bool, error) {
	workspace, err := canonicalWorkspaceDir(rawPath)
	if err != nil {
		return nil, false, err
	}

	name = strings.TrimSpace(name)
	if name == "" {
		name = filepath.Base(workspace)
	}
	if name == "" || name == "." || name == string(filepath.Separator) {
		return nil, false, fmt.Errorf("%w: cannot derive project name from %q", ErrInvalidWorkspace, workspace)
	}
	if len(name) > 255 {
		return nil, false, fmt.Errorf("%w: project name is longer than 255 characters", ErrInvalidWorkspace)
	}

	// The desktop app has one backend process. Serializing this short section
	// prevents two simultaneous open-folder requests from creating duplicates
	// without imposing a new unique index on existing databases.
	s.openMu.Lock()
	defer s.openMu.Unlock()

	existing, err := s.repo.GetByWorkspace(ctx, workspace)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		now := time.Now()
		if err := s.repo.Touch(ctx, existing.ID, now); err != nil {
			return nil, false, err
		}
		existing.UpdatedAt = now
		return existing, false, nil
	}

	// Older rows may contain an absolute but non-canonical spelling (for
	// example, a path through a symlink). Preserve their IDs and reuse them.
	projects, err := s.repo.List(ctx)
	if err != nil {
		return nil, false, err
	}
	for i := range projects {
		canonical, canonicalErr := canonicalWorkspaceDir(projects[i].Workspace)
		if canonicalErr == nil && canonical == workspace {
			now := time.Now()
			if err := s.repo.Touch(ctx, projects[i].ID, now); err != nil {
				return nil, false, err
			}
			projects[i].UpdatedAt = now
			return &projects[i], false, nil
		}
	}

	project := &model.Project{
		ID:        uuid.NewString(),
		Name:      name,
		Workspace: workspace,
	}
	if err := s.repo.Create(ctx, project); err != nil {
		return nil, false, err
	}
	return project, true, nil
}

func canonicalWorkspaceDir(rawPath string) (string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", fmt.Errorf("%w: path is empty", ErrInvalidWorkspace)
	}
	if !filepath.IsAbs(rawPath) {
		return "", fmt.Errorf("%w: path must be absolute", ErrInvalidWorkspace)
	}

	absolute, err := filepath.Abs(filepath.Clean(rawPath))
	if err != nil {
		return "", fmt.Errorf("%w: resolve absolute path: %v", ErrInvalidWorkspace, err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("%w: resolve path: %v", ErrInvalidWorkspace, err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("%w: inspect path: %v", ErrInvalidWorkspace, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: path is not a directory", ErrInvalidWorkspace)
	}
	return filepath.Clean(canonical), nil
}

// Delete removes the project and cascades conversations/messages. The
// workspace is user-owned, so deleting an app record never removes files from
// disk. Streams under the project's conversations are cancelled first.
func (s *ProjectService) Delete(ctx context.Context, id string) error {
	project, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if project == nil {
		return nil
	}

	// Cancel any in-flight streams under this project + close their browser
	// sessions before tearing down DB state.
	if s.convRepo != nil {
		if convs, err := s.convRepo.ListByProject(ctx, id); err == nil {
			for _, c := range convs {
				if s.manager != nil {
					if buf := s.manager.Get(c.ID); buf != nil {
						buf.Cancel()
						s.manager.Remove(c.ID)
					}
				}
				if s.browserMgr != nil {
					s.browserMgr.CloseSession(c.ID)
				}
			}
		}
	}

	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	return nil
}

// OpenInFinder opens the project's workspace directory in the OS file manager.
// macOS only for v1.
func (s *ProjectService) OpenInFinder(ctx context.Context, id string) error {
	p, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("project not found")
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", p.Workspace)
	case "linux":
		cmd = exec.CommandContext(ctx, "xdg-open", p.Workspace)
	case "windows":
		cmd = exec.CommandContext(ctx, "explorer", p.Workspace)
	default:
		return fmt.Errorf("open is not supported on %s", runtime.GOOS)
	}
	return cmd.Start()
}
