package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)

type CreateWorkspaceInput struct {
	Slug string `json:"slug" jsonschema:"description=Workspace directory name. Must match ^[a-z0-9][a-z0-9-]{0\\,40}$ (lowercase letters\\, digits\\, hyphens). Used as the on-disk folder and the project id. Example: 'golang-interview-prep'."`
	Name string `json:"name" jsonschema:"description=Human-readable project name (any language). Shown in the sidebar. Example: 'Go 面试题库'"`
}

type CreateWorkspaceOutput struct {
	ProjectID     string `json:"project_id"`
	ProjectName   string `json:"project_name"`
	WorkspacePath string `json:"workspace_path"`
	Message       string `json:"message"`
}

// NewCreateWorkspaceTool builds the create_workspace tool. The returned tool
// closes over `workspaceRoot` (where directories are created) and the repos
// (to persist the project + bind the current conversation).
func NewCreateWorkspaceTool(
	workspaceRoot string,
	projectRepo *repository.ProjectRepo,
	convRepo *repository.ConversationRepo,
) (tool.BaseTool, error) {
	fn := func(ctx context.Context, in *CreateWorkspaceInput) (*CreateWorkspaceOutput, error) {
		convID := contextkey.ConversationID(ctx)
		if convID == "" {
			return nil, fmt.Errorf("internal error: no conversation in context")
		}

		conv, err := convRepo.Get(ctx, convID)
		if err != nil {
			return nil, fmt.Errorf("load conversation: %w", err)
		}
		if conv != nil && conv.ProjectID != nil && *conv.ProjectID != "" {
			existingSlug := *conv.ProjectID
			existingName := existingSlug
			if p, _ := projectRepo.Get(ctx, existingSlug); p != nil && p.Name != "" {
				existingName = p.Name
			}
			return nil, fmt.Errorf(
				"当前会话已绑定工作区 %q (slug=%s)，不能再次创建工作区。请直接使用相对路径在现有工作区读写文件。",
				existingName, existingSlug,
			)
		}

		if !slugPattern.MatchString(in.Slug) {
			return nil, fmt.Errorf("invalid slug %q: must match ^[a-z0-9][a-z0-9-]{0,40}$", in.Slug)
		}
		name := in.Name
		if name == "" {
			name = in.Slug
		}

		// Reject if project already exists.
		existing, err := projectRepo.Get(ctx, in.Slug)
		if err != nil {
			return nil, fmt.Errorf("check existing project: %w", err)
		}
		if existing != nil {
			return nil, fmt.Errorf("workspace %q already exists; pick another slug", in.Slug)
		}

		absRoot, err := filepath.Abs(workspaceRoot)
		if err != nil {
			return nil, fmt.Errorf("resolve workspace root: %w", err)
		}
		dir := filepath.Join(absRoot, in.Slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create directory: %w", err)
		}

		project := &model.Project{
			ID:        in.Slug,
			Name:      name,
			Workspace: dir,
		}
		if err := projectRepo.Create(ctx, project); err != nil {
			// best-effort rollback the directory we just created
			_ = os.Remove(dir)
			return nil, fmt.Errorf("persist project: %w", err)
		}

		if err := convRepo.SetProjectID(ctx, convID, in.Slug); err != nil {
			return nil, fmt.Errorf("bind conversation to project: %w", err)
		}

		// Emit a project_bound frame so the frontend can update sidebar grouping
		// in real time (in addition to the post-stream refresh fallback).
		if buf := contextkey.Buffer(ctx); buf != nil {
			buf.Append(stream.Encode(stream.Frame{
				Type:          "project_bound",
				ProjectID:     in.Slug,
				ProjectName:   name,
				WorkspacePath: dir,
			}))
		}

		return &CreateWorkspaceOutput{
			ProjectID:     in.Slug,
			ProjectName:   name,
			WorkspacePath: dir,
			Message:       fmt.Sprintf("已创建工作区 %q（%s）。后续文件类工具使用 %s 作为根目录。", name, in.Slug, dir),
		}, nil
	}

	return utils.InferTool(
		"create_workspace",
		"Create a project workspace for this conversation. Use this BEFORE any file or shell operation when the conversation is not yet bound to a workspace. Once bound, this conversation persists under the project and all file paths are relative to its workspace directory.",
		fn,
	)
}
