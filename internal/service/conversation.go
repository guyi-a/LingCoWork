package service

import (
	"context"

	"github.com/guyi-a/Interview-Agent/internal/agent/browseruse"
	"github.com/guyi-a/Interview-Agent/internal/repository"
	"github.com/guyi-a/Interview-Agent/internal/repository/model"
	"github.com/guyi-a/Interview-Agent/internal/stream"
)

type ConversationService struct {
	convRepo       *repository.ConversationRepo
	msgRepo        *repository.MessageRepo
	compactionRepo *repository.CompactionRepo
	manager        *stream.Manager
	browserMgr     *browseruse.Manager
}

func NewConversationService(
	convRepo *repository.ConversationRepo,
	msgRepo *repository.MessageRepo,
	compactionRepo *repository.CompactionRepo,
	manager *stream.Manager,
	browserMgr *browseruse.Manager,
) *ConversationService {
	return &ConversationService{
		convRepo:       convRepo,
		msgRepo:        msgRepo,
		compactionRepo: compactionRepo,
		manager:        manager,
		browserMgr:     browserMgr,
	}
}

func (s *ConversationService) List(ctx context.Context, limit int) ([]model.Conversation, error) {
	return s.convRepo.List(ctx, limit)
}

func (s *ConversationService) Messages(ctx context.Context, id string) ([]model.Message, error) {
	return s.msgRepo.List(ctx, id)
}

// ActiveCompaction returns the fold point the UI should mark, or nil when
// the conversation has never been compacted. A read failure degrades to nil:
// losing the marker is cosmetic, failing the whole history load is not.
func (s *ConversationService) ActiveCompaction(ctx context.Context, id string) *model.Compaction {
	if s.compactionRepo == nil {
		return nil
	}
	c, err := s.compactionRepo.Latest(ctx, id)
	if err != nil {
		return nil
	}
	return c
}

func (s *ConversationService) Delete(ctx context.Context, id string) error {
	if buf := s.manager.Get(id); buf != nil {
		buf.Cancel()
		s.manager.Remove(id)
	}
	if s.browserMgr != nil {
		s.browserMgr.CloseSession(id)
	}
	return s.convRepo.Delete(ctx, id)
}
