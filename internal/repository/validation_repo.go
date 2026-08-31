package repository

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

type ValidationRepo struct {
	db *gorm.DB
}

func NewValidationRepo(db *gorm.DB) *ValidationRepo {
	return &ValidationRepo{db: db}
}

func (r *ValidationRepo) Upsert(ctx context.Context, run *model.ValidationRun) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "conversation_id"},
			{Name: "tool_call_id"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"project_id", "user_message_seq", "command", "cwd", "kind",
			"exit_code", "duration_ms", "passed", "parser", "parse_ok",
			"diagnostics_json", "diagnostics_truncated", "stdout_truncated",
			"stderr_truncated", "timed_out", "created_at",
		}),
	}).Create(run).Error
}

func (r *ValidationRepo) ListCurrent(
	ctx context.Context,
	conversationID string,
	userMessageSeq int,
) ([]model.ValidationRun, error) {
	var runs []model.ValidationRun
	err := r.db.WithContext(ctx).
		Where("conversation_id = ? AND user_message_seq = ?", conversationID, userMessageSeq).
		Order("created_at DESC, id DESC").
		Find(&runs).Error
	return runs, err
}

func (r *ValidationRepo) ListConversation(
	ctx context.Context,
	conversationID string,
	limit int,
) ([]model.ValidationRun, error) {
	var runs []model.ValidationRun
	query := r.db.WithContext(ctx).
		Where("conversation_id = ?", conversationID).
		Order("created_at DESC, id DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	err := query.Find(&runs).Error
	return runs, err
}

func (r *ValidationRepo) DeleteByConversation(
	ctx context.Context,
	conversationID string,
) error {
	return r.db.WithContext(ctx).
		Where("conversation_id = ?", conversationID).
		Delete(&model.ValidationRun{}).Error
}
