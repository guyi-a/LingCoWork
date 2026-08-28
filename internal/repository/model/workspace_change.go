package model

import "time"

// WorkspaceFileBaseline stores a file's state immediately before the first
// Agent mutation in one user-message run. Content is omitted for sensitive,
// binary and oversized files; those rows still retain metadata for honest UI.
type WorkspaceFileBaseline struct {
	ID             uint64 `gorm:"primaryKey;autoIncrement"`
	ProjectID      string `gorm:"type:varchar(64);index;not null"`
	ConversationID string `gorm:"type:varchar(64);uniqueIndex:idx_workspace_baseline,priority:1;not null"`
	UserMessageSeq int    `gorm:"uniqueIndex:idx_workspace_baseline,priority:2;not null"`
	Path           string `gorm:"type:text;uniqueIndex:idx_workspace_baseline,priority:3;not null"`
	Existed        bool
	Content        []byte `gorm:"type:blob"`
	SHA256         string `gorm:"type:varchar(64)"`
	Size           int64
	ModTime        int64
	Mode           uint32
	Binary         bool
	Sensitive      bool
	TooLarge       bool
	CreatedAt      time.Time
}

func (WorkspaceFileBaseline) TableName() string {
	return "workspace_file_baselines"
}

// WorkspaceChangeEvent attributes a successful or partially-successful tool
// operation to a path. The baseline remains the source of diff content; this
// journal explains which tool touched it.
type WorkspaceChangeEvent struct {
	ID             uint64 `gorm:"primaryKey;autoIncrement"`
	ProjectID      string `gorm:"type:varchar(64);index;not null"`
	ConversationID string `gorm:"type:varchar(64);index:idx_workspace_change_run,priority:1;not null"`
	UserMessageSeq int    `gorm:"index:idx_workspace_change_run,priority:2;not null"`
	ToolCallID     string `gorm:"type:varchar(128);index"`
	ToolName       string `gorm:"type:varchar(128);not null"`
	Operation      string `gorm:"type:varchar(32);not null"`
	Path           string `gorm:"type:text"`
	OldPath        string `gorm:"type:text"`
	Attribution    string `gorm:"type:varchar(32);not null;default:agent"`
	Succeeded      bool
	CreatedAt      time.Time
}

func (WorkspaceChangeEvent) TableName() string {
	return "workspace_change_events"
}
