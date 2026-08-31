package workplan

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
)

const (
	OriginPlan  = "plan"
	OriginAgent = "agent"

	StatusDraft     = "draft"
	StatusAwaiting  = "awaiting"
	StatusActive    = "active"
	StatusCompleted = "completed"
	StatusCancelled = "cancelled"

	ItemPending    = "pending"
	ItemInProgress = "in_progress"
	ItemCompleted  = "completed"
	ItemCancelled  = "cancelled"

	maxItems = 100
)

var (
	ErrNotFound          = errors.New("work plan not found")
	ErrConflict          = errors.New("work plan was edited elsewhere")
	ErrNotEditable       = errors.New("work plan is not editable")
	ErrTooManyInProgress = errors.New("at most one work item may be in progress")
	itemIDPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)
)

type Item struct {
	ID       string `json:"id"`
	Content  string `json:"content"`
	Status   string `json:"status"`
	Position int    `json:"position"`
}

type Snapshot struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	UserMessageSeq int    `json:"user_message_seq"`
	Origin         string `json:"origin"`
	Overview       string `json:"overview"`
	BodyMD         string `json:"body_md"`
	Status         string `json:"status"`
	Revision       int    `json:"revision"`
	Items          []Item `json:"items"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type Service struct {
	repo *repository.WorkPlanRepo
}

func NewService(repo *repository.WorkPlanRepo) *Service {
	return &Service{repo: repo}
}

func (s *Service) Latest(ctx context.Context, conversationID string) (*Snapshot, error) {
	if s == nil || s.repo == nil {
		return nil, nil
	}
	plan, items, err := s.repo.Latest(ctx, conversationID)
	if err != nil || plan == nil {
		return nil, err
	}
	out := snapshot(plan, items)
	return &out, nil
}

func (s *Service) List(
	ctx context.Context,
	conversationID string,
) ([]Snapshot, error) {
	if s == nil || s.repo == nil {
		return []Snapshot{}, nil
	}
	plans, err := s.repo.List(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	out := make([]Snapshot, 0, len(plans))
	for i := range plans {
		plan, items, err := s.repo.Get(ctx, conversationID, plans[i].ID)
		if err != nil {
			return nil, err
		}
		if plan != nil {
			out = append(out, snapshot(plan, items))
		}
	}
	return out, nil
}

func (s *Service) Get(
	ctx context.Context,
	conversationID, planID string,
) (*Snapshot, error) {
	plan, items, err := s.repo.Get(ctx, conversationID, planID)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, ErrNotFound
	}
	out := snapshot(plan, items)
	return &out, nil
}

func (s *Service) CreateDraft(
	ctx context.Context,
	conversationID string,
	userMessageSeq int,
	overview, bodyMD string,
	input []Item,
) (*Snapshot, error) {
	items, err := normalizeItems(input)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	current, _, err := s.repo.LatestForUserTurn(ctx, conversationID, userMessageSeq)
	if err != nil {
		return nil, err
	}
	if current != nil && (current.Status == StatusDraft || current.Status == StatusAwaiting) {
		current.Overview = strings.TrimSpace(overview)
		current.BodyMD = strings.TrimSpace(bodyMD)
		current.Status = StatusAwaiting
		current.UpdatedAt = now
		if err := s.repo.Update(ctx, current, toModels(current.ID, items), current.Revision); err != nil {
			return nil, translateConflict(err)
		}
		out := snapshot(current, toModels(current.ID, items))
		return &out, nil
	}

	plan := &model.WorkPlan{
		ID:             uuid.NewString(),
		ConversationID: conversationID,
		UserMessageSeq: userMessageSeq,
		Origin:         OriginPlan,
		Overview:       strings.TrimSpace(overview),
		BodyMD:         strings.TrimSpace(bodyMD),
		Status:         StatusAwaiting,
		Revision:       1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	modelItems := toModels(plan.ID, items)
	if err := s.repo.Create(ctx, plan, modelItems); err != nil {
		return nil, err
	}
	out := snapshot(plan, modelItems)
	return &out, nil
}

func (s *Service) EditDraft(
	ctx context.Context,
	conversationID, planID string,
	expectedRevision int,
	overview, bodyMD string,
	input []Item,
) (*Snapshot, error) {
	plan, _, err := s.repo.Get(ctx, conversationID, planID)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, ErrNotFound
	}
	if plan.Status != StatusDraft && plan.Status != StatusAwaiting {
		return nil, ErrNotEditable
	}
	items, err := normalizeItems(input)
	if err != nil {
		return nil, err
	}
	plan.Overview = strings.TrimSpace(overview)
	plan.BodyMD = strings.TrimSpace(bodyMD)
	plan.Status = StatusAwaiting
	plan.UpdatedAt = time.Now()
	modelItems := toModels(plan.ID, items)
	if err := s.repo.Update(ctx, plan, modelItems, expectedRevision); err != nil {
		return nil, translateConflict(err)
	}
	out := snapshot(plan, modelItems)
	return &out, nil
}

func (s *Service) Activate(
	ctx context.Context,
	conversationID, planID string,
	expectedRevision int,
	overview, bodyMD string,
	input []Item,
) (*Snapshot, error) {
	plan, _, err := s.repo.Get(ctx, conversationID, planID)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, ErrNotFound
	}
	if plan.Status != StatusDraft && plan.Status != StatusAwaiting {
		return nil, ErrNotEditable
	}
	items, err := normalizeItems(input)
	if err != nil {
		return nil, err
	}
	plan.Overview = strings.TrimSpace(overview)
	plan.BodyMD = strings.TrimSpace(bodyMD)
	plan.Status = statusForItems(items, StatusActive)
	plan.UpdatedAt = time.Now()
	modelItems := toModels(plan.ID, items)
	if err := s.repo.Update(ctx, plan, modelItems, expectedRevision); err != nil {
		return nil, translateConflict(err)
	}
	out := snapshot(plan, modelItems)
	return &out, nil
}

func (s *Service) Cancel(
	ctx context.Context,
	conversationID, planID string,
	expectedRevision int,
) (*Snapshot, error) {
	plan, items, err := s.repo.Get(ctx, conversationID, planID)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, ErrNotFound
	}
	plan.Status = StatusCancelled
	plan.UpdatedAt = time.Now()
	for i := range items {
		if items[i].Status == ItemPending || items[i].Status == ItemInProgress {
			items[i].Status = ItemCancelled
		}
	}
	if err := s.repo.Update(ctx, plan, items, expectedRevision); err != nil {
		return nil, translateConflict(err)
	}
	out := snapshot(plan, items)
	return &out, nil
}

// UpdateTodos applies the model-facing todo_write contract. A missing plan ID
// uses the board created for this user turn, or creates an active Agent board.
func (s *Service) UpdateTodos(
	ctx context.Context,
	conversationID string,
	userMessageSeq int,
	planID string,
	merge bool,
	updates []Item,
) (*Snapshot, error) {
	var (
		plan  *model.WorkPlan
		items []model.WorkItem
		err   error
	)
	if strings.TrimSpace(planID) != "" {
		plan, items, err = s.repo.Get(ctx, conversationID, planID)
	} else {
		plan, items, err = s.repo.LatestForUserTurn(ctx, conversationID, userMessageSeq)
	}
	if err != nil {
		return nil, err
	}
	if plan == nil {
		normalized, err := normalizeItems(updates)
		if err != nil {
			return nil, err
		}
		now := time.Now()
		plan = &model.WorkPlan{
			ID:             uuid.NewString(),
			ConversationID: conversationID,
			UserMessageSeq: userMessageSeq,
			Origin:         OriginAgent,
			Status:         statusForItems(normalized, StatusActive),
			Revision:       1,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		modelItems := toModels(plan.ID, normalized)
		if err := s.repo.Create(ctx, plan, modelItems); err != nil {
			return nil, err
		}
		out := snapshot(plan, modelItems)
		return &out, nil
	}
	if plan.Status == StatusCancelled {
		return nil, ErrNotEditable
	}

	current := fromModels(items)
	next := updates
	if merge {
		next = mergeItems(current, updates)
	}
	normalized, err := normalizeItems(next)
	if err != nil {
		return nil, err
	}
	plan.Status = statusForItems(normalized, StatusActive)
	plan.UpdatedAt = time.Now()
	modelItems := toModels(plan.ID, normalized)
	if err := s.repo.Update(ctx, plan, modelItems, plan.Revision); err != nil {
		return nil, translateConflict(err)
	}
	out := snapshot(plan, modelItems)
	return &out, nil
}

func normalizeItems(input []Item) ([]Item, error) {
	if len(input) == 0 {
		return nil, errors.New("work plan requires at least one item")
	}
	if len(input) > maxItems {
		return nil, fmt.Errorf("work plan has %d items; maximum is %d", len(input), maxItems)
	}
	seen := make(map[string]struct{}, len(input))
	inProgress := 0
	out := make([]Item, len(input))
	for i, item := range input {
		item.ID = strings.TrimSpace(item.ID)
		item.Content = strings.TrimSpace(item.Content)
		item.Status = strings.TrimSpace(item.Status)
		if item.Status == "" {
			item.Status = ItemPending
		}
		if !itemIDPattern.MatchString(item.ID) {
			return nil, fmt.Errorf("invalid work item id %q", item.ID)
		}
		if _, ok := seen[item.ID]; ok {
			return nil, fmt.Errorf("duplicate work item id %q", item.ID)
		}
		seen[item.ID] = struct{}{}
		if item.Content == "" {
			return nil, fmt.Errorf("work item %q has empty content", item.ID)
		}
		switch item.Status {
		case ItemPending, ItemInProgress, ItemCompleted, ItemCancelled:
		default:
			return nil, fmt.Errorf("invalid work item status %q", item.Status)
		}
		if item.Status == ItemInProgress {
			inProgress++
		}
		item.Position = i
		out[i] = item
	}
	if inProgress > 1 {
		return nil, ErrTooManyInProgress
	}
	return out, nil
}

func mergeItems(current, updates []Item) []Item {
	out := append([]Item(nil), current...)
	index := make(map[string]int, len(out))
	for i := range out {
		index[out[i].ID] = i
	}
	for _, update := range updates {
		if i, ok := index[strings.TrimSpace(update.ID)]; ok {
			if strings.TrimSpace(update.Content) == "" {
				update.Content = out[i].Content
			}
			if strings.TrimSpace(update.Status) == "" {
				update.Status = out[i].Status
			}
			update.Position = out[i].Position
			out[i] = update
			continue
		}
		update.Position = len(out)
		index[strings.TrimSpace(update.ID)] = len(out)
		out = append(out, update)
	}
	return out
}

func statusForItems(items []Item, fallback string) string {
	for _, item := range items {
		if item.Status == ItemPending || item.Status == ItemInProgress {
			return fallback
		}
	}
	return StatusCompleted
}

func translateConflict(err error) error {
	if errors.Is(err, repository.ErrWorkPlanConflict) {
		return ErrConflict
	}
	return err
}

func toModels(planID string, items []Item) []model.WorkItem {
	out := make([]model.WorkItem, len(items))
	for i, item := range items {
		out[i] = model.WorkItem{
			PlanID:   planID,
			ItemID:   item.ID,
			Content:  item.Content,
			Status:   item.Status,
			Position: i,
		}
	}
	return out
}

func fromModels(items []model.WorkItem) []Item {
	out := make([]Item, len(items))
	for i, item := range items {
		out[i] = Item{
			ID:       item.ItemID,
			Content:  item.Content,
			Status:   item.Status,
			Position: item.Position,
		}
	}
	return out
}

func snapshot(plan *model.WorkPlan, items []model.WorkItem) Snapshot {
	return Snapshot{
		ID:             plan.ID,
		ConversationID: plan.ConversationID,
		UserMessageSeq: plan.UserMessageSeq,
		Origin:         plan.Origin,
		Overview:       plan.Overview,
		BodyMD:         plan.BodyMD,
		Status:         plan.Status,
		Revision:       plan.Revision,
		Items:          fromModels(items),
		CreatedAt:      plan.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:      plan.UpdatedAt.Format(time.RFC3339Nano),
	}
}
