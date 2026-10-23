package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dont/internal/dstruntime"
	"dont/internal/shards"
	"dont/shared"
)

func TestAgentRuntimeMaintenanceIsFencedIdempotentAndPreservesSaves(t *testing.T) {
	control := &fakeShardRuntime{status: shards.RuntimeStatus{State: shards.RuntimeStopped}}
	agent, installation := newShardOperationAgent(t, control)
	world := filepath.Join(installation.SavePath, "Cluster_1", "Master")
	if err := os.MkdirAll(filepath.Join(world, "save"), 0750); err != nil {
		t.Fatal(err)
	}
	save := filepath.Join(world, "save", "world.sav")
	if err := os.WriteFile(save, []byte("original save"), 0600); err != nil {
		t.Fatal(err)
	}
	request := runtimeOperationRequest(shared.RuntimeActionLuaEnsure)
	request.LuaRuntime = &shared.RuntimeLuaRequest{Version: dstruntime.RuntimeVersion, InstallIfMissing: true}
	first, err := agent.executeRuntimeOperation(string(request.Action), &request, 30)
	if err != nil || first.LuaRuntime == nil || !first.LuaRuntime.Changed || first.LuaRuntime.Active {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := agent.executeRuntimeOperation(string(request.Action), &request, 30)
	if err != nil || !second.Idempotent || len(control.calls) != 0 {
		t.Fatalf("replay=%+v err=%v calls=%v", second, err, control.calls)
	}
	data, err := os.ReadFile(save)
	if err != nil || string(data) != "original save" {
		t.Fatalf("save changed: %q %v", data, err)
	}
	loader, err := os.ReadFile(filepath.Join(world, "customcommands.lua"))
	if err != nil || !strings.Contains(string(loader), "mod_config_data/dst-admin/runtime/bootstrap.lua") {
		t.Fatalf("invalid loader: %q %v", loader, err)
	}
	request.OperationID, request.OperationKey = "stale-runtime-update", "stale-runtime-update-key"
	request.FencingToken = 0
	if _, err := agent.executeRuntimeOperation(string(request.Action), &request, 30); err == nil {
		t.Fatal("unfenced maintenance accepted")
	}
}

func TestRuntimeMaintenanceRejectsMixedPayload(t *testing.T) {
	request := runtimeOperationRequest(shared.RuntimeActionLuaEnsure)
	request.LuaRuntime = &shared.RuntimeLuaRequest{Version: dstruntime.RuntimeVersion}
	if err := validateRuntimeOperationRequest(string(request.Action), request, 30, time.Now()); err != nil {
		t.Fatal(err)
	}
	request.Console = &shared.RuntimeConsoleRequest{Mode: shared.ConsoleModeRaw, Command: "print(1)"}
	if err := validateRuntimeOperationRequest(string(request.Action), request, 30, time.Now()); err == nil {
		t.Fatal("mixed maintenance and console payload accepted")
	}
	request.Action = shared.RuntimeActionConsoleSend
	if err := validateRuntimeOperationRequest(string(request.Action), request, 30, time.Now()); err == nil {
		t.Fatal("Lua maintenance payload accepted on console action")
	}
}
