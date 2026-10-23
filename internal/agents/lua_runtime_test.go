package agents

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dont/shared"
)

func TestRuntimeMaintenanceRequiresAgentCapabilityAndCompatibleDocuments(t *testing.T) {
	service, _, _, _ := newAgentTestService(t)
	config := RuntimeConfig{InstallationID: "primary", DisplayName: "node", SavePath: "/srv/dst/save", ServerPath: "/srv/dst/server", ServerMode: "64"}
	if _, err := service.SaveRuntimeConfig("agent-primary", config); err != nil {
		t.Fatal(err)
	}
	request := shared.RuntimeOperationRequest{ProtocolVersion: shared.RuntimeOperationProtocolVersion, OperationID: "runtime-update-1", Action: shared.RuntimeActionLuaEnsure, Cluster: "Cluster_1", Shard: "Master", TopologyRevision: "revision-1", LuaRuntime: &shared.RuntimeLuaRequest{Version: "2.4.11"}}
	if _, err := service.ExecuteRuntime(context.Background(), "agent:agent-primary", request, 30); !errors.Is(err, ErrUnsupportedAction) || !strings.Contains(err.Error(), "升级 Agent") {
		t.Fatalf("legacy maintenance error=%v", err)
	}
	request.Action, request.LuaRuntime = shared.RuntimeActionConsoleSend, nil
	request.Console = &shared.RuntimeConsoleRequest{Mode: shared.ConsoleModeManaged, Command: "print(1)", CommandDocument: &shared.RuntimeCommandDocument{}}
	if _, err := service.ExecuteRuntime(context.Background(), "agent:agent-primary", request, 30); err == nil || !strings.Contains(err.Error(), "升级 Agent") {
		t.Fatalf("legacy document error=%v", err)
	}
}

func TestRuntimeNodeSignalsIgnoreHeartbeatsAndObserveAgentRestart(t *testing.T) {
	service, _, _, transport := newAgentTestService(t)
	if _, err := service.Sync(); err != nil {
		t.Fatal(err)
	}
	var changes atomic.Int32
	service.AddRuntimeNodeListener(func() { changes.Add(1) })
	set := func(instance string, heartbeat time.Time) {
		transport.mu.Lock()
		snapshot := transport.snapshots["agent-primary"]
		snapshot.Details = map[string]interface{}{"runtime_instance_id": instance}
		snapshot.LastHeartbeat = heartbeat
		transport.snapshots["agent-primary"] = snapshot
		transport.mu.Unlock()
		if _, err := service.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	set("boot-a", time.Now())
	first := changes.Load()
	set("boot-a", time.Now().Add(time.Second))
	if changes.Load() != first {
		t.Fatalf("heartbeat triggered maintenance: %d -> %d", first, changes.Load())
	}
	set("boot-b", time.Now().Add(2*time.Second))
	if changes.Load() != first+1 {
		t.Fatalf("Agent restart did not trigger maintenance: %d -> %d", first, changes.Load())
	}
}
