package repository

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

type MessageRepo struct {
	db *gorm.DB
	mu sync.Mutex
}

// AppendEvent writes one protocol message at its completion boundary. EventKey
// makes retries and resumed callbacks idempotent; legacy callers may continue
// using Append with a nil key.
func (r *MessageRepo) AppendEvent(
	ctx context.Context,
	m *model.Message,
) (bool, error) {
	if m == nil {
		return false, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if m.EventKey == nil || *m.EventKey == "" {
		return true, r.append(ctx, m)
	}
	created := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.Message
		result := tx.Select("id", "seq", "created_at").
			Where("conversation_id = ? AND event_key = ?", m.ConversationID, *m.EventKey).
			Limit(1).
			Find(&existing)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			m.ID, m.Seq, m.CreatedAt = existing.ID, existing.Seq, existing.CreatedAt
			return nil
		}
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
		result = tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "conversation_id"},
				{Name: "event_key"},
			},
			DoNothing: true,
		}).Create(m)
		if result.Error != nil {
			return result.Error
		}
		created = result.RowsAffected == 1
		return nil
	})
	return created, err
}

func (r *MessageRepo) UpdateAssistantMetadata(
	ctx context.Context,
	id uint64,
	totalTokens int,
	extra string,
) error {
	if id == 0 {
		return nil
	}
	updates := map[string]any{}
	if totalTokens > 0 {
		updates["total_tokens"] = totalTokens
	}
	if extra != "" {
		updates["extra"] = extra
	}
	if len(updates) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&model.Message{}).
		Where("id = ?", id).
		Updates(updates).Error
}

func (r *MessageRepo) LatestAssistantID(
	ctx context.Context,
	conversationID string,
) (uint64, error) {
	var row model.Message
	err := r.db.WithContext(ctx).
		Select("id").
		Where("conversation_id = ? AND role = ?", conversationID, "assistant").
		Order("seq DESC").
		First(&row).Error
	if err == gorm.ErrRecordNotFound {
		return 0, nil
	}
	return row.ID, err
}

type OpenToolCall struct {
	ID       string
	Name     string
	ArgsJSON string
}

// OpenToolCalls returns persisted assistant tool calls that have no matching
// tool row. It is used only for crash/cancel reconciliation and never executes
// the calls again.
func (r *MessageRepo) OpenToolCalls(
	ctx context.Context,
	conversationID string,
) ([]OpenToolCall, error) {
	rows, err := r.List(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	resolved := make(map[string]struct{})
	for _, row := range rows {
		if row.Role == "tool" && row.ToolCallID != "" {
			resolved[row.ToolCallID] = struct{}{}
		}
	}
	var out []OpenToolCall
	seen := make(map[string]struct{})
	for _, row := range rows {
		if row.Role != "assistant" || row.ToolCalls == "" {
			continue
		}
		var calls []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			ArgsJSON string `json:"args_json"`
		}
		if json.Unmarshal([]byte(row.ToolCalls), &calls) != nil {
			continue
		}
		for _, call := range calls {
			if call.ID == "" {
				continue
			}
			if _, ok := resolved[call.ID]; ok {
				continue
			}
			if _, ok := seen[call.ID]; ok {
				continue
			}
			seen[call.ID] = struct{}{}
			out = append(out, OpenToolCall{
				ID: call.ID, Name: call.Name, ArgsJSON: call.ArgsJSON,
			})
		}
	}
	return out, nil
}

func NewMessageRepo(db *gorm.DB) *MessageRepo {
	return &MessageRepo{db: db}
}

// Append inserts a message with the next seq for the conversation.
// Uses a transaction to compute seq + insert atomically (avoids races within the same process).
func (r *MessageRepo) Append(ctx context.Context, m *model.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.append(ctx, m)
}

func (r *MessageRepo) append(ctx context.Context, m *model.Message) error {
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
	r.mu.Lock()
	defer r.mu.Unlock()
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

func (r *MessageRepo) MaxSeq(ctx context.Context, conversationID string) (int, error) {
	var seq int
	err := r.db.WithContext(ctx).
		Model(&model.Message{}).
		Where("conversation_id = ?", conversationID).
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
