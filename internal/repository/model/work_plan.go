package model

import "time"

// WorkPlan is the durable task container shared by Plan mode and Agent todos.
// A Plan-mode draft becomes the active execution board in place when the user
// starts implementation; direct Agent runs create an active origin=agent row.
type WorkPlan struct {
	ID             string `gorm:"primaryKey;type:varchar(64)"`
	ConversationID string `gorm:"type:varchar(64);index:idx_work_plan_conv_updated,priority:1;not null"`
	UserMessageSeq int    `gorm:"index;not null"`
	Origin         string `gorm:"type:varchar(16);not null"`
	Overview       string `gorm:"type:text"`
	BodyMD         string `gorm:"type:text"`
	Status         string `gorm:"type:varchar(24);index;not null"`
	Revision       int    `gorm:"not null;default:1"`
	CreatedAt      time.Time
	UpdatedAt      time.Time `gorm:"index:idx_work_plan_conv_updated,priority:2"`
}

func (WorkPlan) TableName() string {
	return "work_plans"
}

// WorkItem is one stable checklist item within a WorkPlan. ItemID comes from
// the model/plan editor and remains stable across merge updates; ID is only
// the database surrogate key.
type WorkItem struct {
	ID        uint64 `gorm:"primaryKey;autoIncrement"`
	PlanID    string `gorm:"type:varchar(64);not null;uniqueIndex:idx_work_item_plan_key,priority:1;index"`
	ItemID    string `gorm:"type:varchar(96);not null;uniqueIndex:idx_work_item_plan_key,priority:2"`
	Content   string `gorm:"type:text;not null"`
	Status    string `gorm:"type:varchar(20);index;not null"`
	Position  int    `gorm:"not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (WorkItem) TableName() string {
	return "work_items"
}
