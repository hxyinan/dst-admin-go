package dstruntime

import (
	"context"
	"sync"
	"testing"
	"time"

	"dont/internal/operationlease"
	"dont/internal/rooms"
	"dont/shared"
)

type maintenanceTestCatalog struct{ runtimeTestCatalog }

func (c maintenanceTestCatalog) List() ([]rooms.Room, error) { return []rooms.Room{c.room}, nil }

type maintenanceTestRuntime struct {
	mu    sync.Mutex
	busy  bool
	calls chan string
}

func (r *maintenanceTestRuntime) EnsureLuaRuntime(_ context.Context, _, worldID string, request shared.RuntimeLuaRequest) (shared.RuntimeLuaResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls <- worldID
	if r.busy && worldID == rooms.EncodeID("Master") {
		r.busy = false
		return shared.RuntimeLuaResult{}, operationlease.ErrBusy
	}
	return shared.RuntimeLuaResult{Version: request.Version, State: "installed", Active: true, Message: "updated"}, nil
}

func TestAutoUpdateCoalescesSignalsRetriesBusyWorldAndStaysIdle(t *testing.T) {
	_, catalog, _ := newRuntimeTestManager(t)
	runtime := &maintenanceTestRuntime{busy: true, calls: make(chan string, 16)}
	updater := NewAutoUpdater(maintenanceTestCatalog{catalog}, runtime)
	for i := 0; i < 100; i++ {
		updater.Wake()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); updater.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	next := func(timeout time.Duration) string {
		t.Helper()
		select {
		case world := <-runtime.calls:
			return world
		case <-time.After(timeout):
			t.Fatal("maintenance did not run")
			return ""
		}
	}
	first, second := next(time.Second), next(time.Second)
	if first != rooms.EncodeID("Master") || second != rooms.EncodeID("Caves") {
		t.Fatalf("busy world blocked other worlds: %s,%s", first, second)
	}
	if world := next(2 * time.Second); world != rooms.EncodeID("Master") {
		t.Fatalf("retried world=%s", world)
	}
	select {
	case extra := <-runtime.calls:
		t.Fatalf("coalesced events or idle loop repeated %s", extra)
	case <-time.After(60 * time.Millisecond):
	}
	updater.Wake()
	next(time.Second)
	next(time.Second)
}
