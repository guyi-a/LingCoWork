package model

import "time"

type Conversation struct {
	ID           string  `gorm:"primaryKey;type:varchar(64)"`
	ProjectID    *string `gorm:"type:varchar(64);index"` // nil = ad-hoc, non-nil = project-bound
	Title        string  `gorm:"type:varchar(255)"`
	Status       string  `gorm:"type:varchar(20);default:'active';index"`
	AgentStatus  string  `gorm:"type:varchar(20);default:'idle';index"` // idle / running / waiting_approval / waiting_user
	ChatMode     string  `gorm:"type:varchar(12);not null;default:'agent'"`
	ApprovalMode string  `gorm:"type:varchar(16);not null;default:'manual'"`
	Pinned       bool    `gorm:"default:false"`
	CreatedAt    time.Time
	UpdatedAt    time.Time `gorm:"index"`
}

func (Conversation) TableName() string {
	return "conversations"
}
