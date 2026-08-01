package repository

import (
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

func NewDB(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(
		&model.Project{},
		&model.Conversation{},
		&model.Message{},
		&model.Checkpoint{},
		&model.PendingApproval{},
		&model.Compaction{},
	); err != nil {
		return nil, err
	}
	return db, nil
}
