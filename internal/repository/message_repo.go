package repository

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

type MessageRepo struct {
	db *gorm.DB
}

func NewMessageRepo(db *gorm.DB) *MessageRepo {
	return &MessageRepo{db: db}
}

// Append inserts a message with the next seq for the conversation.
// Uses a transaction to compute seq + insert atomically (avoids races within the same process).
func (r *MessageRepo) Append(ctx context.Context, m *model.Message) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxSeq int
		if err := tx.Model(&model.Message{}).
			Where("conversation_id = ?", m.ConversationID).
			Select("COALESCE(MAX(seq), 0)").
			Scan(&maxSeq).Error; err != nil {
			return err
		}
		m.Seq = maxSeq + 1
		if m.CreatedAt.IsZero() {
			m.CreatedAt = time.Now()
		}
		return tx.Create(m).Error
	})
}

// AppendMany inserts a batch of messages atomically. All messages in the
// batch share the same conversation_id (taken from msgs[0]); seq is assigned
// contiguously starting from MAX(seq)+1. On any failure the entire batch is
// rolled back — critical for tool_use/tool_result pairing: a partial batch
// would leave a stranded assistant tool_call without its tool_result rows,
// and the next turn would get 400 from Claude on replay.
//
// Empty batch is a no-op.
func (r *MessageRepo) AppendMany(ctx context.Context, msgs []*model.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	convID := msgs[0].ConversationID
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxSeq int
		if err := tx.Model(&model.Message{}).
			Where("conversation_id = ?", convID).
			Select("COALESCE(MAX(seq), 0)").
			Scan(&maxSeq).Error; err != nil {
			return err
		}
		now := time.Now()
		for i, m := range msgs {
			m.Seq = maxSeq + 1 + i
			if m.CreatedAt.IsZero() {
				m.CreatedAt = now
			}
			if err := tx.Create(m).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *MessageRepo) LatestUserSeq(ctx context.Context, conversationID string) (int, error) {
	var seq int
	err := r.db.WithContext(ctx).
		Model(&model.Message{}).
		Where("conversation_id = ? AND role = ?", conversationID, "user").
		Select("COALESCE(MAX(seq), 0)").
		Scan(&seq).Error
	return seq, err
}

// List returns all messages of a conversation in seq order.
func (r *MessageRepo) List(ctx context.Context, conversationID string) ([]model.Message, error) {
	var out []model.Message
	err := r.db.WithContext(ctx).
		Where("conversation_id = ?", conversationID).
		Order("seq ASC").
		Find(&out).Error
	return out, err
}
