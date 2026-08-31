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
}

func NewBuffer() *StreamBuffer {
	return &StreamBuffer{status: StatusStreaming}
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
	out := make(chan []byte, 16)

	b.mu.Lock()
	history := make([][]byte, len(b.chunks))
	copy(history, b.chunks)
	if b.status == StatusComplete {
		b.mu.Unlock()
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
	b.mu.Unlock()

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
	m.mu.Lock()
	defer m.mu.Unlock()
	buf := NewBuffer()
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
