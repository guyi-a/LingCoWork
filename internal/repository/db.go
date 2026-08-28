package repository

import (
	"os"
	"strings"

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
		&model.MCPCredential{},
		&model.WorkspaceFileBaseline{},
		&model.WorkspaceChangeEvent{},
	); err != nil {
		return nil, err
	}
	restrictDBPermissions(dsn)
	return db, nil
}

// restrictDBPermissions narrows the database file to the owner.
//
// It holds OAuth refresh tokens for third-party services, which are long-
// lived account credentials; sqlite creates the file 0644 and every other
// account on the machine could read them. Best-effort: the dsn may carry
// query parameters or name a non-file target, and a permissions failure is
// not a reason to refuse to start.
func restrictDBPermissions(dsn string) {
	path, _, _ := strings.Cut(dsn, "?")
	if path == "" || path == ":memory:" {
		return
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Chmod(path+suffix, 0o600)
	}
}
