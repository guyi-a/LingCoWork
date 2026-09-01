package approval

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/guyi-a/Interview-Agent/internal/repository"
)

// Mode names the per-conversation approval policy. The set is closed —
// unknown strings from the HTTP layer are rejected in Set / ValidMode.
type Mode string

const (
	ModeManual      Mode = "manual"
	ModeAcceptWrite Mode = "accept-write"
	ModeAuto        Mode = "auto"
)

func ValidMode(m Mode) bool {
	switch m {
	case ModeManual, ModeAcceptWrite, ModeAuto:
		return true
	default:
		return false
	}
}

// ModeStore is a read-through, write-through cache over conversations.
// Approval modes are a durable conversation preference. Any repository
// failure falls back to manual rather than widening permissions.
type ModeStore struct {
	mu   sync.RWMutex
	byID map[string]Mode
	repo *repository.ConversationRepo
}

func NewModeStore(repo *repository.ConversationRepo) *ModeStore {
	return &ModeStore{byID: make(map[string]Mode), repo: repo}
}

// Get returns the persisted mode for convID, or ModeManual on missing/invalid
// data and repository failures.
func (s *ModeStore) Get(convID string) Mode {
	if s == nil || convID == "" {
		return ModeManual
	}
	s.mu.RLock()
	if m, ok := s.byID[convID]; ok {
		s.mu.RUnlock()
		return m
	}
	s.mu.RUnlock()

	mode := ModeManual
	if s.repo != nil {
		conv, err := s.repo.Get(context.Background(), convID)
		if err != nil {
			log.Printf("approval: load mode for %s: %v; falling back to manual", convID, err)
		} else if conv != nil {
			candidate := Mode(conv.ApprovalMode)
			if ValidMode(candidate) {
				mode = candidate
			}
		}
	}
	s.mu.Lock()
	s.byID[convID] = mode
	s.mu.Unlock()
	return mode
}

func (s *ModeStore) Set(convID string, m Mode) error {
	if s == nil {
		return fmt.Errorf("mode store not initialised")
	}
	if convID == "" {
		return fmt.Errorf("conversation id required")
	}
	if !ValidMode(m) {
		return fmt.Errorf("invalid mode %q", string(m))
	}
	if s.repo != nil {
		if err := s.repo.SetApprovalMode(context.Background(), convID, string(m)); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.byID[convID] = m
	s.mu.Unlock()
	return nil
}
