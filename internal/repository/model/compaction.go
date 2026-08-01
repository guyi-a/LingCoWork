package model

import "time"

// Compaction is one context-compaction checkpoint for a conversation.
//
// Compaction is append-only: message rows are never deleted or rewritten.
// A Compaction row says "everything up to and including ThroughSeq has been
// folded into Summary". The LLM context projection swaps those rows for a
// single synthetic message; the UI still renders the full history and just
// draws a marker at the fold point.
//
// The newest row per conversation is the active anchor. Older rows are kept
// for observability — a second compaction folds only messages with
// Seq > previous.ThroughSeq and carries the previous Summary forward as
// <prior-summary>, so summaries chain rather than compound.
type Compaction struct {
	ID             uint64 `gorm:"primaryKey;autoIncrement"`
	ConversationID string `gorm:"type:varchar(64);index:idx_comp_conv,priority:1;not null"`
	// ThroughSeq folds messages with Seq <= ThroughSeq.
	ThroughSeq int    `gorm:"index:idx_comp_conv,priority:2;not null"`
	Summary    string `gorm:"type:text;not null"`
	// ReplacedCount and EstimatedTokens are metadata only — how many rows
	// this fold covered and what the estimator read just before it ran.
	ReplacedCount   int
	EstimatedTokens int
	CreatedAt       time.Time
}

func (Compaction) TableName() string {
	return "compactions"
}
