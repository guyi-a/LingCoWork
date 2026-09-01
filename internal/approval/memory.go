package approval

import (
	"context"
	"sync"
)

// Memory stores explicit "allow for this conversation" grants in-process.
// It deliberately does not survive restart and never crosses conversations.
type Memory struct {
	mu   sync.RWMutex
	byID map[string]map[string]struct{}
}

func NewMemory() *Memory {
	return &Memory{byID: make(map[string]map[string]struct{})}
}

func (m *Memory) Remember(convID, fingerprint string) {
	if m == nil || convID == "" || fingerprint == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.byID[convID] == nil {
		m.byID[convID] = make(map[string]struct{})
	}
	m.byID[convID][fingerprint] = struct{}{}
}

func (m *Memory) Allowed(convID string) map[string]struct{} {
	out := make(map[string]struct{})
	if m == nil || convID == "" {
		return out
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for fingerprint := range m.byID[convID] {
		out[fingerprint] = struct{}{}
	}
	return out
}

func (m *Memory) Count(convID string) int {
	if m == nil || convID == "" {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.byID[convID])
}

func (m *Memory) Clear(convID string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	delete(m.byID, convID)
	m.mu.Unlock()
}

type memorySnapshotKey struct{}

// WithMemorySnapshot freezes the grants visible to one run/continuation.
func WithMemorySnapshot(ctx context.Context, allowed map[string]struct{}) context.Context {
	copyOf := make(map[string]struct{}, len(allowed))
	for fingerprint := range allowed {
		copyOf[fingerprint] = struct{}{}
	}
	return context.WithValue(ctx, memorySnapshotKey{}, copyOf)
}

func rememberedInSnapshot(ctx context.Context, fingerprint string) bool {
	allowed, _ := ctx.Value(memorySnapshotKey{}).(map[string]struct{})
	_, ok := allowed[fingerprint]
	return ok
}
