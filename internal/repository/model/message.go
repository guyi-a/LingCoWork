package model

import "time"

type Message struct {
	ID               uint64    `gorm:"primaryKey;autoIncrement"`
	ConversationID   string    `gorm:"type:varchar(64);index:idx_conv_seq,priority:1;not null"`
	Seq              int       `gorm:"index:idx_conv_seq,priority:2;not null"`
	Role             string    `gorm:"type:varchar(20);not null"`
	Content          string    `gorm:"type:text"`
	ReasoningContent string    `gorm:"type:text"`
	ToolCalls        string    `gorm:"type:text"`
	ToolCallID       string    `gorm:"type:varchar(64)"`
	ToolName         string    `gorm:"type:varchar(128)"`
	Extra            string    `gorm:"type:text"`
	// TotalTokens is the provider-reported context size after this message,
	// recorded only on root-agent assistant rows. The compaction estimator
	// uses the most recent non-zero value as its baseline instead of
	// character-counting the whole history. Sub-agent usage is deliberately
	// excluded: it measures that agent's own private context, not the
	// main thread's.
	TotalTokens int `gorm:"default:0"`
	CreatedAt   time.Time
}

func (Message) TableName() string {
	return "messages"
}
