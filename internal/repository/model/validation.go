package model

import "time"

// ValidationRun is one structured test/build/lint/typecheck/format command.
// Diagnostics stay as bounded JSON because the panel reads complete runs and
// never queries individual diagnostics independently.
type ValidationRun struct {
	ID                   uint64 `gorm:"primaryKey;autoIncrement"`
	ProjectID            string `gorm:"type:varchar(64);index;not null"`
	ConversationID       string `gorm:"type:varchar(64);not null;uniqueIndex:idx_validation_conv_call,priority:1;index:idx_validation_conv_seq,priority:1"`
	UserMessageSeq       int    `gorm:"not null;index:idx_validation_conv_seq,priority:2"`
	ToolCallID           string `gorm:"type:varchar(128);not null;uniqueIndex:idx_validation_conv_call,priority:2"`
	Command              string `gorm:"type:text;not null"`
	Cwd                  string `gorm:"type:text"`
	Kind                 string `gorm:"type:varchar(16);not null"`
	ExitCode             int
	DurationMs           int64
	Passed               bool
	Parser               string `gorm:"type:varchar(32)"`
	ParseOK              bool
	DiagnosticsJSON      string `gorm:"type:text"`
	DiagnosticsTruncated bool
	StdoutTruncated      bool
	StderrTruncated      bool
	TimedOut             bool
	CreatedAt            time.Time `gorm:"index"`
}

func (ValidationRun) TableName() string {
	return "validation_runs"
}
