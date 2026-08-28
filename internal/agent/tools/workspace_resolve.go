package tools

import (
	"context"
	"fmt"

	"github.com/guyi-a/Interview-Agent/internal/agent/contextkey"
	"github.com/guyi-a/Interview-Agent/internal/repository"
)

// resolveConversationWorkspace returns the absolute workspace path selected
// for the current conversation. Workspace selection is a user-facing UI
// action; tools never create or silently choose directories.
func resolveConversationWorkspace(
	ctx context.Context,
	convRepo *repository.ConversationRepo,
	projectRepo *repository.ProjectRepo,
) (string, error) {
	convID := contextkey.ConversationID(ctx)
	if convID == "" {
		return "", fmt.Errorf("internal error: no conversation in context")
	}
	conv, err := convRepo.Get(ctx, convID)
	if err != nil {
		return "", fmt.Errorf("load conversation: %w", err)
	}
	if conv == nil || conv.ProjectID == nil || *conv.ProjectID == "" {
		return "", fmt.Errorf("no workspace selected for this conversation; ask the user to choose a folder in the app")
	}
	project, err := projectRepo.Get(ctx, *conv.ProjectID)
	if err != nil {
		return "", fmt.Errorf("load project: %w", err)
	}
	if project == nil {
		return "", fmt.Errorf("project %q referenced by conversation no longer exists", *conv.ProjectID)
	}
	return project.Workspace, nil
}
