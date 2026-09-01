package stream

import (
	"context"
	"sync"
)

type Status int

const (
	StatusNone Status = iota
	StatusStreaming
	StatusComplete
)

type StreamBuffer struct {
	mu          sync.RWMutex
	chunks      [][]byte
	subscribers []chan []byte
	status      Status
	cancel      context.CancelFunc
	durableSeq  int
}

func NewBuffer() *StreamBuffer {
	return NewBufferAt(0)
}

func NewBufferAt(durableSeq int) *StreamBuffer {
	return &StreamBuffer{status: StatusStreaming, durableSeq: durableSeq}
}

func (b *StreamBuffer) SetCancel(cancel context.CancelFunc) {
	b.mu.Lock()
	b.cancel = cancel
	b.mu.Unlock()
}

func (b *StreamBuffer) Cancel() bool {
	b.mu.Lock()
	c := b.cancel
	b.mu.Unlock()
	if c == nil {
		return false
	}
	c()
	return true
}

func (b *StreamBuffer) Append(chunk []byte) {
	cp := make([]byte, len(chunk))
	copy(cp, chunk)

	b.mu.Lock()
	if b.status != StatusStreaming {
		b.mu.Unlock()
		return
	}
	b.chunks = append(b.chunks, cp)
	for _, ch := range b.subscribers {
		select {
		case ch <- cp:
		default:
		}
	}
	b.mu.Unlock()
}

// PublishLive sends a durable event to current subscribers without retaining
// it for reconnect replay. A reconnect loads completed message boundaries
// from the DB first, so replaying this event would duplicate them.
func (b *StreamBuffer) PublishLive(chunk []byte) {
	cp := append([]byte(nil), chunk...)
	b.mu.RLock()
	if b.status != StatusStreaming {
		b.mu.RUnlock()
		return
	}
	for _, ch := range b.subscribers {
		select {
		case ch <- cp:
		default:
		}
	}
	b.mu.RUnlock()
}

// ClearReplay drops already-durable history without affecting connected
// subscribers. Only unfinished assistant deltas remain replayable.
func (b *StreamBuffer) ClearReplay() {
	b.mu.Lock()
	b.chunks = nil
	b.mu.Unlock()
}

// CommitBoundary atomically advances the DB frontier and removes replay data
// that is now represented by durable messages.
func (b *StreamBuffer) CommitBoundary(seq int) {
	b.mu.Lock()
	if seq > b.durableSeq {
		b.durableSeq = seq
	}
	b.chunks = nil
	b.mu.Unlock()
}

func (b *StreamBuffer) DurableSeq() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.durableSeq
}

type CursorStatus string

const (
	CursorEqual        CursorStatus = "equal"
	CursorClientStale  CursorStatus = "client_stale"
	CursorBufferBehind CursorStatus = "buffer_behind"
	CursorComplete     CursorStatus = "complete"
)

func (b *StreamBuffer) Finish() {
	b.mu.Lock()
	if b.status == StatusComplete {
		b.mu.Unlock()
		return
	}
	b.status = StatusComplete
	subs := b.subscribers
	b.subscribers = nil
	b.mu.Unlock()

	for _, ch := range subs {
		close(ch)
	}
}

func (b *StreamBuffer) Status() Status {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.status
}

func (b *StreamBuffer) StreamAll(ctx context.Context) <-chan []byte {
	b.mu.Lock()
	out := b.subscribeLocked(ctx)
	b.mu.Unlock()
	return out
}

// StreamFrom compares the client's DB snapshot and registers the subscriber
// in one critical section. Only an exact cursor match may consume replay.
func (b *StreamBuffer) StreamFrom(
	ctx context.Context,
	afterSeq int,
) (<-chan []byte, CursorStatus, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.status == StatusComplete {
		return nil, CursorComplete, b.durableSeq
	}
	if afterSeq < b.durableSeq {
		return nil, CursorClientStale, b.durableSeq
	}
	if afterSeq > b.durableSeq {
		return nil, CursorBufferBehind, b.durableSeq
	}
	return b.subscribeLocked(ctx), CursorEqual, b.durableSeq
}

// subscribeLocked snapshots replay and registers for future frames while the
// caller holds b.mu, closing the check/subscribe race.
func (b *StreamBuffer) subscribeLocked(ctx context.Context) <-chan []byte {
	out := make(chan []byte, 16)
	history := make([][]byte, len(b.chunks))
	copy(history, b.chunks)
	if b.status == StatusComplete {
		go func() {
			defer close(out)
			for _, c := range history {
				select {
				case <-ctx.Done():
					return
				case out <- c:
				}
			}
		}()
		return out
	}

	sub := make(chan []byte, 64)
	b.subscribers = append(b.subscribers, sub)

	go func() {
		defer close(out)
		defer b.unsubscribe(sub)

		for _, c := range history {
			select {
			case <-ctx.Done():
				return
			case out <- c:
			}
		}
		for {
			select {
			case <-ctx.Done():
				return
			case c, ok := <-sub:
				if !ok {
					return
				}
				select {
				case <-ctx.Done():
					return
				case out <- c:
				}
			}
		}
	}()

	return out
}

func (b *StreamBuffer) unsubscribe(target chan []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, ch := range b.subscribers {
		if ch == target {
			b.subscribers = append(b.subscribers[:i], b.subscribers[i+1:]...)
			return
		}
	}
}

type Manager struct {
	mu      sync.RWMutex
	buffers map[string]*StreamBuffer
}

func NewManager() *Manager {
	return &Manager{buffers: make(map[string]*StreamBuffer)}
}

func (m *Manager) Get(id string) *StreamBuffer {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.buffers[id]
}

func (m *Manager) Create(id string) *StreamBuffer {
	return m.CreateAt(id, 0)
}

func (m *Manager) CreateAt(id string, durableSeq int) *StreamBuffer {
	m.mu.Lock()
	defer m.mu.Unlock()
	buf := NewBufferAt(durableSeq)
	m.buffers[id] = buf
	return buf
}

func (m *Manager) IsStreaming(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	buf, ok := m.buffers[id]
	return ok && buf.Status() == StatusStreaming
}

func (m *Manager) Remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.buffers, id)
}

// ShutdownAll cancels every active stream and marks its buffer complete.
// Used during graceful shutdown to release producer goroutines + close subscribers.
func (m *Manager) ShutdownAll() {
	m.mu.Lock()
	bufs := make([]*StreamBuffer, 0, len(m.buffers))
	for _, b := range m.buffers {
		bufs = append(bufs, b)
	}
	m.mu.Unlock()

	for _, b := range bufs {
		b.Cancel()
		b.Finish()
	}
}
