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
