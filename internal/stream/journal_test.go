package stream

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

type recordingJournal struct {
	assistants []AssistantTurnRecord
	tools      []ToolResultRecord
	fail       error
	seq        int
}

func TestAssistantPersistenceFailureStopsBeforeToolPublication(t *testing.T) {
	buf := NewBuffer()
	journal := &recordingJournal{fail: errors.New("disk unavailable")}
	router := &subAgentRouter{rootName: "root", active: map[string]string{}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sub := buf.StreamAll(ctx)
	msg := &schema.Message{
		Role: schema.Assistant, Content: "partial",
		ToolCalls: []schema.ToolCall{{
			ID:       "call-1",
			Function: schema.FunctionCall{Name: "write_file", Arguments: `{}`},
		}},
	}
	err := drainAssistantStream(
		t.Context(), true, "root", router,
		schema.StreamReaderFromArray([]*schema.Message{msg}),
		buf, NewRunCollector(), journal,
	)
	if err == nil {
		t.Fatal("persistence failure did not stop assistant boundary")
	}
	if frame := string(receive(t, sub)); frame == "" {
		t.Fatal("streamed text was not delivered")
	}
	select {
	case frame := <-sub:
		t.Fatalf("tool call published despite persistence failure: %s", frame)
	default:
	}
}

func (j *recordingJournal) AppendAssistant(_ context.Context, record AssistantTurnRecord) (int, bool, error) {
	if j.fail != nil {
		return 0, false, j.fail
	}
	j.assistants = append(j.assistants, record)
	j.seq++
	return j.seq, true, nil
}

func (j *recordingJournal) AppendToolResult(_ context.Context, record ToolResultRecord) (int, bool, error) {
	if j.fail != nil {
		return 0, false, j.fail
	}
	j.tools = append(j.tools, record)
	j.seq++
	return j.seq, true, nil
}

func (*recordingJournal) AppendPartialAssistant(context.Context, string, string) error {
	return nil
}

func (*recordingJournal) UpdateLastAssistant(context.Context, int, string) error {
	return nil
}

// countingJournal records how many times UpdateLastAssistant ran, so the
// dirty-gating in flushSubEvents can be asserted.
type countingJournal struct {
	lastAssistantCalls int
}

func (*countingJournal) AppendAssistant(context.Context, AssistantTurnRecord) (int, bool, error) {
	return 1, true, nil
}
func (*countingJournal) AppendToolResult(context.Context, ToolResultRecord) (int, bool, error) {
	return 2, true, nil
}
func (*countingJournal) AppendPartialAssistant(context.Context, string, string) error {
	return nil
}
func (j *countingJournal) UpdateLastAssistant(context.Context, int, string) error {
	j.lastAssistantCalls++
	return nil
}

type rearmingJournal struct {
	countingJournal
	collector *RunCollector
}

func (j *rearmingJournal) UpdateLastAssistant(ctx context.Context, tokens int, extra string) error {
	j.lastAssistantCalls++
	if j.lastAssistantCalls == 1 {
		j.collector.AppendSubEvent(SubAgentEvent{
			Agent: "deep_research", Type: "text", Content: "arrived during flush",
		})
	}
	return nil
}

func TestFlushSubEventsWritesOnlyWhenDirty(t *testing.T) {
	collector := NewRunCollector()
	journal := &countingJournal{}

	// No sub-agent events yet → nothing dirty, no write.
	if err := flushSubEvents(t.Context(), collector, journal); err != nil {
		t.Fatal(err)
	}
	if journal.lastAssistantCalls != 0 {
		t.Fatalf("wrote before any sub event: %d", journal.lastAssistantCalls)
	}

	collector.AppendSubEvent(SubAgentEvent{Agent: "deep_research", Type: "text", Content: "first"})
	if err := flushSubEvents(t.Context(), collector, journal); err != nil {
		t.Fatal(err)
	}
	if journal.lastAssistantCalls != 1 {
		t.Fatalf("writes=%d, want 1 after a new sub event", journal.lastAssistantCalls)
	}

	// No new sub events → dirty cleared, second flush is a no-op.
	if err := flushSubEvents(t.Context(), collector, journal); err != nil {
		t.Fatal(err)
	}
	if journal.lastAssistantCalls != 1 {
		t.Fatalf("writes=%d, want still 1 (no new sub event)", journal.lastAssistantCalls)
	}

	// A second sub event re-arms the dirty flag.
	collector.AppendSubEvent(SubAgentEvent{Agent: "deep_research", Type: "text", Content: "second"})
	if err := flushSubEvents(t.Context(), collector, journal); err != nil {
		t.Fatal(err)
	}
	if journal.lastAssistantCalls != 2 {
		t.Fatalf("writes=%d, want 2 after a second new sub event", journal.lastAssistantCalls)
	}
}

func TestFlushSubEventsDoesNotLoseConcurrentUpdate(t *testing.T) {
	collector := NewRunCollector()
	collector.AppendSubEvent(SubAgentEvent{
		Agent: "deep_research", Type: "text", Content: "first",
	})
	journal := &rearmingJournal{collector: collector}
	if err := flushSubEvents(t.Context(), collector, journal); err != nil {
		t.Fatal(err)
	}
	if err := flushSubEvents(t.Context(), collector, journal); err != nil {
		t.Fatal(err)
	}
	if journal.lastAssistantCalls != 2 {
		t.Fatalf("writes=%d, concurrent event was marked clean", journal.lastAssistantCalls)
	}
}

func TestDrainAssistantPersistsBoundaryAndClearsReplay(t *testing.T) {
	buf := NewBuffer()
	collector := NewRunCollector()
	journal := &recordingJournal{}
	router := &subAgentRouter{rootName: "root", active: map[string]string{}}
	msg := &schema.Message{
		Role:    schema.Assistant,
		Content: "hello",
		ToolCalls: []schema.ToolCall{{
			ID: "call-1",
			Function: schema.FunctionCall{
				Name: "read_file", Arguments: `{"path":"a"}`,
			},
		}},
	}
	if err := drainAssistantStream(
		t.Context(), true, "root", router,
		schema.StreamReaderFromArray([]*schema.Message{msg}),
		buf, collector, journal,
	); err != nil {
		t.Fatal(err)
	}
	if len(journal.assistants) != 1 ||
		journal.assistants[0].ToolCalls[0].ID != "call-1" {
		t.Fatalf("assistant journal=%#v", journal.assistants)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	replay, status, durable := buf.StreamFrom(ctx, 1)
	if status != CursorEqual || durable != 1 {
		t.Fatalf("status=%s durable=%d", status, durable)
	}
	select {
	case frame := <-replay:
		t.Fatalf("durable assistant frame remained replayable: %s", frame)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestEmitToolResultPersistsBeforePublishing(t *testing.T) {
	buf := NewBuffer()
	journal := &recordingJournal{}
	router := &subAgentRouter{rootName: "root", active: map[string]string{}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sub := buf.StreamAll(ctx)
	err := emitToolResult(
		t.Context(), true, "root", router, "read_file",
		&schema.Message{Role: schema.Tool, ToolCallID: "call-1", Content: "ok"},
		buf, NewRunCollector(), journal,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.tools) != 1 {
		t.Fatalf("tool journal=%#v", journal.tools)
	}
	if frame := receive(t, sub); len(frame) == 0 {
		t.Fatal("connected subscriber did not receive tool result")
	}
	replay, status, _ := buf.StreamFrom(ctx, 1)
	if status != CursorEqual {
		t.Fatalf("replay status=%s", status)
	}
	select {
	case frame := <-replay:
		t.Fatalf("durable tool result remained replayable: %s", frame)
	case <-time.After(20 * time.Millisecond):
	}
}
