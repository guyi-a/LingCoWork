package repository

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

type WorkspaceChangeRepo struct {
	db *gorm.DB
}

func NewWorkspaceChangeRepo(db *gorm.DB) *WorkspaceChangeRepo {
	return &WorkspaceChangeRepo{db: db}
}

func (r *WorkspaceChangeRepo) CreateBaselineIfAbsent(
	ctx context.Context,
	baseline *model.WorkspaceFileBaseline,
) (bool, error) {
	result := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "conversation_id"},
				{Name: "user_message_seq"},
				{Name: "path"},
			},
			DoNothing: true,
		}).
		Create(baseline)
	return result.RowsAffected > 0, result.Error
}

func (r *WorkspaceChangeRepo) CreateEvent(
	ctx context.Context,
	event *model.WorkspaceChangeEvent,
) error {
	return r.db.WithContext(ctx).Create(event).Error
}

func (r *WorkspaceChangeRepo) ListBaselines(
	ctx context.Context,
	conversationID string,
	userMessageSeq int,
) ([]model.WorkspaceFileBaseline, error) {
	var rows []model.WorkspaceFileBaseline
	err := r.db.WithContext(ctx).
		Where("conversation_id = ? AND user_message_seq = ?", conversationID, userMessageSeq).
		Order("path ASC").
		Find(&rows).Error
	return rows, err
}

func (r *WorkspaceChangeRepo) GetBaseline(
	ctx context.Context,
	conversationID string,
	userMessageSeq int,
	path string,
) (*model.WorkspaceFileBaseline, error) {
	var row model.WorkspaceFileBaseline
	err := r.db.WithContext(ctx).
		Where("conversation_id = ? AND user_message_seq = ? AND path = ?",
			conversationID, userMessageSeq, path).
		First(&row).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	return &row, err
}

func (r *WorkspaceChangeRepo) ListEvents(
	ctx context.Context,
	conversationID string,
	userMessageSeq int,
) ([]model.WorkspaceChangeEvent, error) {
	var rows []model.WorkspaceChangeEvent
	err := r.db.WithContext(ctx).
		Where("conversation_id = ? AND user_message_seq = ?", conversationID, userMessageSeq).
		Order("id ASC").
		Find(&rows).Error
	return rows, err
}
