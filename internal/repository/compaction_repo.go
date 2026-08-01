package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

type CompactionRepo struct {
	db *gorm.DB
}

func NewCompactionRepo(db *gorm.DB) *CompactionRepo {
	return &CompactionRepo{db: db}
}

// Latest returns the active compaction anchor for a conversation, or
// (nil, nil) when the conversation has never been compacted.
//
// Ordered by through_seq rather than id: id order and fold order always
// agree today, but through_seq is what the projection actually keys on, so
// keying the lookup on it too keeps the two from ever drifting apart.
func (r *CompactionRepo) Latest(ctx context.Context, conversationID string) (*model.Compaction, error) {
	var c model.Compaction
	err := r.db.WithContext(ctx).
		Where("conversation_id = ?", conversationID).
		Order("through_seq DESC").
		First(&c).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

func (r *CompactionRepo) Append(ctx context.Context, c *model.Compaction) error {
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
	}
	return r.db.WithContext(ctx).Create(c).Error
}

func (r *CompactionRepo) DeleteByConversation(ctx context.Context, conversationID string) error {
	return r.db.WithContext(ctx).
		Where("conversation_id = ?", conversationID).
		Delete(&model.Compaction{}).Error
}
