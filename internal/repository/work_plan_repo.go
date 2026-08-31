package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

var ErrWorkPlanConflict = errors.New("work plan revision conflict")

type WorkPlanRepo struct {
	db *gorm.DB
}

func NewWorkPlanRepo(db *gorm.DB) *WorkPlanRepo {
	return &WorkPlanRepo{db: db}
}

func (r *WorkPlanRepo) Create(
	ctx context.Context,
	plan *model.WorkPlan,
	items []model.WorkItem,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(plan).Error; err != nil {
			return err
		}
		for i := range items {
			items[i].PlanID = plan.ID
		}
		if len(items) == 0 {
			return nil
		}
		return tx.Create(&items).Error
	})
}

func (r *WorkPlanRepo) Get(
	ctx context.Context,
	conversationID, planID string,
) (*model.WorkPlan, []model.WorkItem, error) {
	var plan model.WorkPlan
	err := r.db.WithContext(ctx).
		Where("id = ? AND conversation_id = ?", planID, conversationID).
		First(&plan).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	items, err := r.items(ctx, plan.ID)
	return &plan, items, err
}

func (r *WorkPlanRepo) Latest(
	ctx context.Context,
	conversationID string,
) (*model.WorkPlan, []model.WorkItem, error) {
	var plan model.WorkPlan
	err := r.db.WithContext(ctx).
		Where("conversation_id = ?", conversationID).
		Order("updated_at DESC, created_at DESC").
		First(&plan).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	items, err := r.items(ctx, plan.ID)
	return &plan, items, err
}

func (r *WorkPlanRepo) List(
	ctx context.Context,
	conversationID string,
) ([]model.WorkPlan, error) {
	var plans []model.WorkPlan
	err := r.db.WithContext(ctx).
		Where("conversation_id = ?", conversationID).
		Order("user_message_seq ASC, created_at ASC").
		Find(&plans).Error
	return plans, err
}

func (r *WorkPlanRepo) LatestForUserTurn(
	ctx context.Context,
	conversationID string,
	userMessageSeq int,
) (*model.WorkPlan, []model.WorkItem, error) {
	var plan model.WorkPlan
	err := r.db.WithContext(ctx).
		Where("conversation_id = ? AND user_message_seq = ?", conversationID, userMessageSeq).
		Order("updated_at DESC, created_at DESC").
		First(&plan).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	items, err := r.items(ctx, plan.ID)
	return &plan, items, err
}

func (r *WorkPlanRepo) Update(
	ctx context.Context,
	plan *model.WorkPlan,
	items []model.WorkItem,
	expectedRevision int,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.WorkPlan{}).
			Where("id = ? AND conversation_id = ? AND revision = ?",
				plan.ID, plan.ConversationID, expectedRevision).
			Updates(map[string]any{
				"overview":   plan.Overview,
				"body_md":    plan.BodyMD,
				"status":     plan.Status,
				"revision":   expectedRevision + 1,
				"updated_at": plan.UpdatedAt,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrWorkPlanConflict
		}
		if err := tx.Where("plan_id = ?", plan.ID).Delete(&model.WorkItem{}).Error; err != nil {
			return err
		}
		for i := range items {
			items[i].PlanID = plan.ID
		}
		if len(items) > 0 {
			if err := tx.Create(&items).Error; err != nil {
				return err
			}
		}
		plan.Revision = expectedRevision + 1
		return nil
	})
}

func (r *WorkPlanRepo) DeleteByConversation(
	ctx context.Context,
	conversationID string,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ids []string
		if err := tx.Model(&model.WorkPlan{}).
			Where("conversation_id = ?", conversationID).
			Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) > 0 {
			if err := tx.Where("plan_id IN ?", ids).Delete(&model.WorkItem{}).Error; err != nil {
				return err
			}
		}
		return tx.Where("conversation_id = ?", conversationID).
			Delete(&model.WorkPlan{}).Error
	})
}

func (r *WorkPlanRepo) items(
	ctx context.Context,
	planID string,
) ([]model.WorkItem, error) {
	var items []model.WorkItem
	err := r.db.WithContext(ctx).
		Where("plan_id = ?", planID).
		Order("position ASC, id ASC").
		Find(&items).Error
	return items, err
}
