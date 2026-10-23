package runtimedriver

import (
	"context"
	"errors"
	"testing"

	"dont/internal/agents"
	"dont/internal/dstruntime"
	"dont/shared"
)

func TestAgentLuaMaintenancePreservesDeferredAndRestoredState(t *testing.T) {
	for _, test := range []struct {
		name      string
		value     *shared.RuntimeLuaResult
		outcome   shared.RuntimeOutcome
		err       error
		deferred  bool
		wantError bool
	}{
		{name: "deferred", value: &shared.RuntimeLuaResult{State: "installed", Version: "2.4.10", Deferred: true}, err: errors.New("remote busy"), deferred: true, wantError: true},
		{name: "restored after failure", value: &shared.RuntimeLuaResult{State: "outdated", Version: "2.4.10"}, err: errors.New("activation failed"), wantError: true},
		{name: "failed outcome", value: &shared.RuntimeLuaResult{State: "installed"}, outcome: shared.RuntimeOutcomeFailed, wantError: true},
		{name: "missing result", outcome: shared.RuntimeOutcomeConfirmed, wantError: true},
		{name: "confirmed", value: &shared.RuntimeLuaResult{State: "installed", Version: dstruntime.RuntimeVersion, Active: true}, outcome: shared.RuntimeOutcomeConfirmed},
	} {
		t.Run(test.name, func(t *testing.T) {
			driver, err := NewAgent(failedMigrationExecutor{result: agents.RuntimeExecutionResult{Result: shared.RuntimeOperationResult{LuaRuntime: test.value, Outcome: test.outcome}}, err: test.err})
			if err != nil {
				t.Fatal(err)
			}
			value, err := driver.EnsureLuaRuntime(context.Background(), Target{TargetID: "agent:node", InstallationID: "default", Cluster: "Cluster_1", Shard: "Master"}, Operation{}, shared.RuntimeLuaRequest{Version: dstruntime.RuntimeVersion})
			if (err != nil) != test.wantError || errors.Is(err, dstruntime.ErrRuntimeRefreshDeferred) != test.deferred {
				t.Fatalf("result=%+v err=%v", value, err)
			}
			if test.value != nil && value != *test.value {
				t.Fatalf("node result lost: %+v", value)
			}
		})
	}
}
