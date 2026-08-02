package model

import "time"

// PendingApproval mirrors an in-flight paused tool-call so it survives a
// process restart. Written when the middleware or a tool fires tool.Interrupt,
// deleted when the user's response is applied (or the conversation is cleared).
// Composite unique on (conversation_id, interrupt_id) — we do not assume
// eino's interrupt id is globally unique, only unique within a checkpoint.
//
// 表名保留 pending_approvals（历史命名），但实际承载两种 Kind：
//   - approval：等待用户对某个工具调用做批准 / 拒绝
//   - question：等待用户回答 ask_user 抛出的问题
//
// Kind 缺省 approval，兼容老落盘行。
type PendingApproval struct {
	ID             uint   `gorm:"primaryKey;autoIncrement"`
	ConversationID string `gorm:"size:64;not null;uniqueIndex:idx_pending_conv_int,priority:1;index"`
	InterruptID    string `gorm:"size:128;not null;uniqueIndex:idx_pending_conv_int,priority:2"`
	CallID         string `gorm:"size:128"`
	Kind           string `gorm:"size:16;not null;default:'approval'"`
	Tool           string `gorm:"size:128"`
	Args           string `gorm:"type:text"`
	// Effect is the marshalled effect.Effect the approval decision was made
	// from. Nullable on purpose: rows written before the column existed
	// restore with an empty value, and the card falls back to summarising
	// Args the way it always did.
	Effect    string    `gorm:"type:text"`
	CreatedAt time.Time `gorm:"index"`
}

func (PendingApproval) TableName() string {
	return "pending_approvals"
}
