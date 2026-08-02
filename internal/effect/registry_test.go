package effect

import (
	"context"
	"strconv"
	"sync"
	"testing"
)

// The registry used to be a plain map filled in at startup and never touched
// again. MCP broke that: a server connects, disconnects or reconnects
// whenever the user edits the connector settings, or finishes an OAuth flow,
// while conversations are mid-run and deriving effects. Run this one with
// -race; without the lock it fails there and nowhere else.
func TestRegistryConcurrentRegisterAndDerive(t *testing.T) {
	reg := NewRegistry()
	reg.Register("stable", Static(Effect{Kind: KindFileRead}))

	const n = 200
	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			reg.Register("remote_"+strconv.Itoa(i), Static(Effect{Kind: KindMCPCall}))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			reg.Unregister("remote_" + strconv.Itoa(i))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if e := reg.Derive(context.Background(), "stable", "{}"); e.Kind != KindFileRead {
				t.Errorf("Derive returned %q", e.Kind)
				return
			}
			reg.Has("remote_" + strconv.Itoa(i))
		}
	}()
	wg.Wait()
}

func TestUnregisterRemovesTheDeriver(t *testing.T) {
	reg := NewRegistry()
	reg.Register("wiki__lookup", Static(Effect{Kind: KindMCPCall}))
	if !reg.Has("wiki__lookup") {
		t.Fatal("Register did not take")
	}

	reg.Unregister("wiki__lookup")

	if reg.Has("wiki__lookup") {
		t.Fatal("Unregister left the deriver behind")
	}
	// The safety property: a withdrawn tool falls back to unknown, which the
	// policy always sends to a human, rather than keeping whatever trust its
	// old server had been granted.
	if got := reg.Derive(context.Background(), "wiki__lookup", "{}"); got.Kind != KindUnknown {
		t.Fatalf("derived %q after unregistering, want unknown", got.Kind)
	}
}
