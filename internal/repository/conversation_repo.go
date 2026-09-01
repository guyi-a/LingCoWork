package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

type ConversationRepo struct {
	db *gorm.DB
}

func NewConversationRepo(db *gorm.DB) *ConversationRepo {
	return &ConversationRepo{db: db}
}

func (r *ConversationRepo) Get(ctx context.Context, id string) (*model.Conversation, error) {
	var c model.Conversation
	if err := r.db.WithContext(ctx).First(&c, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

// Upsert creates the conversation if it doesn't exist, otherwise just bumps updated_at.
func (r *ConversationRepo) Upsert(ctx context.Context, id string) error {
	now := time.Now()
	c := &model.Conversation{
		ID:        id,
		Status:    "active",
		CreatedAt: now,
		UpdatedAt: now,
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.Assignments(map[string]any{"updated_at": now}),
	}).Create(c).Error
}

// SetTitleIfEmpty sets the title only when it's empty (typical: first user message).
func (r *ConversationRepo) SetTitleIfEmpty(ctx context.Context, id, title string) error {
	return r.db.WithContext(ctx).Model(&model.Conversation{}).
		Where("id = ? AND (title IS NULL OR title = '')", id).
		Update("title", title).Error
}

// SetProjectID attaches a conversation to a project. Idempotent — caller is
// responsible for ensuring the project exists.
func (r *ConversationRepo) SetProjectID(ctx context.Context, conversationID, projectID string) error {
	return r.db.WithContext(ctx).Model(&model.Conversation{}).
		Where("id = ?", conversationID).
		Update("project_id", projectID).Error
}

// SetAgentStatus flips the conversation's runtime state (idle / running /
// waiting_approval). Called by ChatService at run start, on interrupt, and
// on finalize so the sidebar can reflect activity without polling.
func (r *ConversationRepo) SetAgentStatus(ctx context.Context, id, status string) error {
	return r.db.WithContext(ctx).Model(&model.Conversation{}).
		Where("id = ?", id).
		Update("agent_status", status).Error
}

func (r *ConversationRepo) SetChatMode(ctx context.Context, id, mode string) error {
	return r.db.WithContext(ctx).Model(&model.Conversation{}).
		Where("id = ?", id).
		Update("chat_mode", mode).Error
}

func (r *ConversationRepo) SetApprovalMode(ctx context.Context, id, mode string) error {
	return r.db.WithContext(ctx).Model(&model.Conversation{}).
		Where("id = ?", id).
		Update("approval_mode", mode).Error
}

// ListByProject returns all conversations belonging to a project.
func (r *ConversationRepo) ListByProject(ctx context.Context, projectID string) ([]model.Conversation, error) {
	var out []model.Conversation
	err := r.db.WithContext(ctx).
		Where("project_id = ?", projectID).
		Order("updated_at DESC").
		Find(&out).Error
	return out, err
}

// List returns project-bound conversations ordered by updated_at desc. Legacy
// unbound rows are intentionally hidden: the product no longer has ad-hoc
// conversations, and new rows cannot be created without a project.
func (r *ConversationRepo) List(ctx context.Context, limit int) ([]model.Conversation, error) {
	var out []model.Conversation
	q := r.db.WithContext(ctx).
		Where("status = ? AND project_id IS NOT NULL AND project_id <> ''", "active").
		Order("updated_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (r *ConversationRepo) ListByAgentStatuses(
	ctx context.Context,
	statuses ...string,
) ([]model.Conversation, error) {
	if len(statuses) == 0 {
		return []model.Conversation{}, nil
	}
	var out []model.Conversation
	err := r.db.WithContext(ctx).
		Where("agent_status IN ?", statuses).
		Order("updated_at ASC").
		Find(&out).Error
	return out, err
}

func (r *ConversationRepo) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("conversation_id = ?", id).Delete(&model.Message{}).Error; err != nil {
			return err
		}
		if err := tx.Where("conversation_id = ?", id).Delete(&model.Compaction{}).Error; err != nil {
			return err
		}
		if err := tx.Where("conversation_id = ?", id).Delete(&model.PendingApproval{}).Error; err != nil {
			return err
		}
		if err := tx.Where("id = ?", id).Delete(&model.Checkpoint{}).Error; err != nil {
			return err
		}
		if err := tx.Where("conversation_id = ?", id).Delete(&model.WorkspaceFileBaseline{}).Error; err != nil {
			return err
		}
		if err := tx.Where("conversation_id = ?", id).Delete(&model.WorkspaceChangeEvent{}).Error; err != nil {
			return err
		}
		var planIDs []string
		if err := tx.Model(&model.WorkPlan{}).
			Where("conversation_id = ?", id).
			Pluck("id", &planIDs).Error; err != nil {
			return err
		}
		if len(planIDs) > 0 {
			if err := tx.Where("plan_id IN ?", planIDs).Delete(&model.WorkItem{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("conversation_id = ?", id).Delete(&model.WorkPlan{}).Error; err != nil {
			return err
		}
		if err := tx.Where("conversation_id = ?", id).Delete(&model.ValidationRun{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Conversation{}, "id = ?", id).Error
	})
}
