package stream

import (
	"context"
	"testing"
	"time"
)

func TestBufferSeparatesReplayableAndLiveOnlyEvents(t *testing.T) {
	buf := NewBuffer()
	buf.Append([]byte("partial"))
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	first := buf.StreamAll(ctx1)
	if got := receive(t, first); string(got) != "partial" {
		t.Fatalf("first replay=%q", got)
	}

	buf.ClearReplay()
	buf.PublishLive([]byte("durable"))
	if got := receive(t, first); string(got) != "durable" {
		t.Fatalf("live event=%q", got)
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	second := buf.StreamAll(ctx2)
	select {
	case got := <-second:
		t.Fatalf("new subscriber replayed durable event %q", got)
	case <-time.After(20 * time.Millisecond):
	}

	buf.Append([]byte("next-partial"))
	if got := receive(t, second); string(got) != "next-partial" {
		t.Fatalf("new partial=%q", got)
	}
}

func TestBufferCursorRequiresExactDurableSequence(t *testing.T) {
	buf := NewBufferAt(5)
	buf.Append([]byte("partial"))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	if ch, status, durable := buf.StreamFrom(ctx, 4); ch != nil ||
		status != CursorClientStale || durable != 5 {
		t.Fatalf("stale = ch:%v status:%s durable:%d", ch, status, durable)
	}
	if ch, status, durable := buf.StreamFrom(ctx, 6); ch != nil ||
		status != CursorBufferBehind || durable != 5 {
		t.Fatalf("ahead = ch:%v status:%s durable:%d", ch, status, durable)
	}
	ch, status, durable := buf.StreamFrom(ctx, 5)
	if status != CursorEqual || durable != 5 {
		t.Fatalf("equal status:%s durable:%d", status, durable)
	}
	if got := string(receive(t, ch)); got != "partial" {
		t.Fatalf("partial=%q", got)
	}

	buf.CommitBoundary(7)
	if buf.DurableSeq() != 7 {
		t.Fatalf("durable=%d", buf.DurableSeq())
	}
	buf.PublishLive([]byte("live"))
	if got := string(receive(t, ch)); got != "live" {
		t.Fatalf("live=%q", got)
	}
	fresh, status, _ := buf.StreamFrom(ctx, 7)
	if status != CursorEqual {
		t.Fatalf("fresh status=%s", status)
	}
	select {
	case got := <-fresh:
		t.Fatalf("fresh replayed durable frame %q", got)
	case <-time.After(20 * time.Millisecond):
	}
}

func receive(t *testing.T, ch <-chan []byte) []byte {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for buffer event")
		return nil
	}
}
