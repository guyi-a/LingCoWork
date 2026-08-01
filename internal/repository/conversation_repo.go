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

// ListByProject returns all conversations belonging to a project.
func (r *ConversationRepo) ListByProject(ctx context.Context, projectID string) ([]model.Conversation, error) {
	var out []model.Conversation
	err := r.db.WithContext(ctx).
		Where("project_id = ?", projectID).
		Order("updated_at DESC").
		Find(&out).Error
	return out, err
}

// List returns conversations ordered by updated_at desc (sidebar order).
func (r *ConversationRepo) List(ctx context.Context, limit int) ([]model.Conversation, error) {
	var out []model.Conversation
	q := r.db.WithContext(ctx).Where("status = ?", "active").Order("updated_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (r *ConversationRepo) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("conversation_id = ?", id).Delete(&model.Message{}).Error; err != nil {
			return err
		}
		if err := tx.Where("conversation_id = ?", id).Delete(&model.Compaction{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Conversation{}, "id = ?", id).Error
	})
}
