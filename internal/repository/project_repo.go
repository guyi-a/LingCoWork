package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

type ProjectRepo struct {
	db *gorm.DB
}

func NewProjectRepo(db *gorm.DB) *ProjectRepo {
	return &ProjectRepo{db: db}
}

func (r *ProjectRepo) Get(ctx context.Context, id string) (*model.Project, error) {
	var p model.Project
	if err := r.db.WithContext(ctx).First(&p, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// GetByWorkspace returns the project bound to the canonical absolute path.
// Workspace paths are assigned by ProjectService, which normalizes them
// before persistence.
func (r *ProjectRepo) GetByWorkspace(ctx context.Context, workspace string) (*model.Project, error) {
	var p model.Project
	if err := r.db.WithContext(ctx).First(&p, "workspace = ?", workspace).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

func (r *ProjectRepo) Create(ctx context.Context, p *model.Project) error {
	now := time.Now()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	return r.db.WithContext(ctx).Create(p).Error
}

func (r *ProjectRepo) List(ctx context.Context) ([]model.Project, error) {
	var out []model.Project
	err := r.db.WithContext(ctx).Order("updated_at DESC").Find(&out).Error
	return out, err
}

// UpdateName changes the project's display name. The slug (id) and workspace
// path remain immutable.
func (r *ProjectRepo) UpdateName(ctx context.Context, id, name string) error {
	return r.db.WithContext(ctx).Model(&model.Project{}).
		Where("id = ?", id).
		Updates(map[string]any{"name": name, "updated_at": time.Now()}).Error
}

func (r *ProjectRepo) Touch(ctx context.Context, id string, updatedAt time.Time) error {
	return r.db.WithContext(ctx).Model(&model.Project{}).
		Where("id = ?", id).
		Update("updated_at", updatedAt).Error
}

// Delete removes the project row + cascades to its conversations and messages
// in a single transaction. Workspace directory cleanup is the caller's
// responsibility (filesystem side-effect kept out of the repo).
func (r *ProjectRepo) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Find conversations under this project.
		var convIDs []string
		if err := tx.Model(&model.Conversation{}).
			Where("project_id = ?", id).
			Pluck("id", &convIDs).Error; err != nil {
			return err
		}
		if len(convIDs) > 0 {
			if err := tx.Where("conversation_id IN ?", convIDs).
				Delete(&model.Message{}).Error; err != nil {
				return err
			}
			if err := tx.Where("project_id = ?", id).
				Delete(&model.Conversation{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("project_id = ?", id).Delete(&model.WorkspaceFileBaseline{}).Error; err != nil {
			return err
		}
		if err := tx.Where("project_id = ?", id).Delete(&model.WorkspaceChangeEvent{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Project{}, "id = ?", id).Error
	})
}
