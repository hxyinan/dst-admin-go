package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"dont/internal/agents"
	"dont/internal/aicontrol"
	"dont/internal/dstruntime"
	"dont/internal/rooms"
	"dont/internal/runtimeobservation"
	"dont/internal/runtimeoverview"
	"dont/internal/topology"
	"dont/shared"
)

type aiOverviewSource struct {
	topology topology.Snapshot
	statuses map[string]shared.ShardRuntimeStatus
	failures map[string]error
	health   map[string]dstruntime.Health
}

type aiObservedTopology struct {
	plan         topology.Snapshot
	observations *runtimeobservation.Coordinator
}

func (s aiObservedTopology) Topology(ctx context.Context, roomID string) (topology.Snapshot, error) {
	items, err := s.observations.RuntimeTargetInventories(ctx)
	if err != nil {
		return topology.Snapshot{}, err
	}
	value := s.plan
	value.Targets = append([]topology.TargetSummary(nil), s.plan.Targets...)
	value.Placements = append([]topology.Placement(nil), s.plan.Placements...)
	for i, target := range value.Targets {
		for _, item := range items {
			if target.ID != item.Target.ID {
				continue
			}
			value.Targets[i].InventoryAvailable = item.Available
			value.Targets[i].InventoryStale = item.Stale
			for j, placement := range value.Placements {
				if placement.AppliedTargetID == target.ID && !item.Available {
					value.Placements[j].State = topology.PlacementInventoryMissing
				}
			}
		}
	}
	return value, nil
}

func TestAIRoomStatusRefreshesOnlyItsMissingInventoryAndReusesFreshObservations(t *testing.T) {
	now := time.Now().UTC()
	local := runtimeObservationHTTPInventory(now)
	local.Target.ID = "local"
	local.Target.Kind = agents.RuntimeKindLocal
	local.Target.Config.InstallationID = "default"
	local.Inventory.Installation.ID = "default"
	local.Available = false
	unrelated := runtimeObservationHTTPInventory(now)
	source := &runtimeObservationHTTPSource{items: []agents.RuntimeTargetInventory{local, unrelated}, collect: func(_ context.Context, item agents.RuntimeTargetInventory) (agents.RuntimeTargetInventory, error) {
		item.Available = true
		item.Stale = false
		return item, nil
	}}
	coordinator, err := runtimeobservation.New(source)
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()
	fixture := newAIOverviewFixture()
	for i := range fixture.topology.Placements {
		fixture.topology.Placements[i].AppliedInstallationID = "default"
		fixture.topology.Placements[i].DesiredInstallationID = "default"
	}
	service, err := runtimeoverview.New(aiObservedTopology{plan: fixture.topology, observations: coordinator}, fixture, fixture)
	if err != nil {
		t.Fatal(err)
	}
	backend := &AIBackend{overview: &RuntimeObservabilityHandler{overview: service, observations: coordinator}}
	for i := 0; i < 2; i++ {
		raw, err := backend.Invoke(context.Background(), "room_status", aicontrol.Arguments{RoomID: "room"}, "")
		if err != nil || strings.Contains(string(raw), `"state":"inventory_missing"`) || !strings.Contains(string(raw), `"state":"healthy"`) {
			t.Fatal("fresh observation was not used", string(raw), err)
		}
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.collects != 1 || len(source.collectedTargetIDs) != 1 || source.collectedTargetIDs[0] != "local" {
		t.Fatal("refreshed fresh or unrelated targets", source.collects, source.collectedTargetIDs)
	}
}

func (s aiOverviewSource) Topology(context.Context, string) (topology.Snapshot, error) {
	return s.topology, nil
}
func (s aiOverviewSource) Status(_ context.Context, _, worldID string) (shared.ShardRuntimeStatus, error) {
	return s.statuses[worldID], s.failures[worldID]
}
func (s aiOverviewSource) Health(_ context.Context, _, worldID string) (dstruntime.Health, error) {
	if value, ok := s.health[worldID]; ok {
		return value, nil
	}
	return dstruntime.Health{}, dstruntime.ErrSnapshotUnavailable
}
func (s aiOverviewSource) LatestDiagnostic(context.Context, string, string) (dstruntime.DiagnosticReport, error) {
	return dstruntime.DiagnosticReport{}, dstruntime.ErrRuntimeResultAbsent
}

func newAIOverviewFixture() aiOverviewSource {
	now := time.Now().UTC()
	return aiOverviewSource{
		topology: topology.Snapshot{
			RoomID: "room", Revision: "revision",
			Targets: []topology.TargetSummary{{
				ID: "local", Name: "本机", Kind: agents.RuntimeKindLocal, Online: true, Configured: true,
				InventoryAvailable: true, ObservedAt: &now, ObservedRunningShards: 1, PlannedShards: 4, ProjectedShards: 4,
				CurrentCapacity:   agents.Capacity{State: agents.CapacityAvailable, RunningShards: 1, RecommendedShardLimit: 3, MemoryAvailableBytes: 2 << 30},
				ProjectedCapacity: agents.Capacity{State: agents.CapacityOvercommitted, RunningShards: 4, ProjectedMemoryAvailableBytes: 63 << 20},
			}},
			Placements: []topology.Placement{
				{WorldID: "master", WorldName: "Master", WorldRole: rooms.WorldRoleMaster, AppliedTargetID: "local", DesiredTargetID: "local", State: topology.PlacementAligned},
				{WorldID: "caves", WorldName: "Caves", WorldRole: rooms.WorldRoleCaves, AppliedTargetID: "local", DesiredTargetID: "local", State: topology.PlacementAligned},
			},
			Issues: []topology.Issue{{Code: "TARGET_MEMORY_CRITICAL", Severity: topology.SeverityWarning, TargetID: "local", Message: "预计内存不足"}},
		},
		statuses: map[string]shared.ShardRuntimeStatus{"master": {State: "running", SessionExists: true}, "caves": {State: "stopped"}},
		health:   map[string]dstruntime.Health{"master": {Ready: true, Running: true, ReadAt: now}},
	}
}

func (source aiOverviewSource) backend(t *testing.T) *AIBackend {
	t.Helper()
	service, err := runtimeoverview.New(source, source, source)
	if err != nil {
		t.Fatal(err)
	}
	return &AIBackend{overview: &RuntimeObservabilityHandler{overview: service}}
}

func TestAIRoomStatusSeparatesCurrentStateFromMissingHistoryAndStartupPlan(t *testing.T) {
	backend := newAIOverviewFixture().backend(t)
	raw, err := backend.Invoke(context.Background(), "room_status", aicontrol.Arguments{RoomID: "room"}, "room")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Summary runtimeoverview.Summary `json:"summary"`
		Shards  []struct {
			WorldID   string `json:"worldId"`
			State     string `json:"state"`
			Telemetry struct {
				Status string `json:"status"`
				Ready  bool   `json:"ready"`
			} `json:"telemetry"`
		} `json:"shards"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Summary.Running != 1 || result.Summary.Stopped != 1 || len(result.Shards) != 2 || result.Shards[0].State != "healthy" || !result.Shards[0].Telemetry.Ready || result.Shards[1].State != "stopped" || result.Shards[1].Telemetry.Status != "not_expected" {
		t.Fatalf("current state is ambiguous: %s", raw)
	}
	for _, absent := range []string{"RUNTIME_ARTIFACT_MISSING", "RUNTIME_RESULT_NOT_FOUND", "latestDiagnostic", "TARGET_MEMORY_CRITICAL", "projectedCapacity", "startupPlan"} {
		if strings.Contains(string(raw), absent) {
			t.Errorf("routine status mixes in %s: %s", absent, raw)
		}
	}
	if len(raw) > 8<<10 {
		t.Fatal("routine two-world status exceeds model excerpt budget", len(raw))
	}
	raw, err = backend.Invoke(context.Background(), "room_status", aicontrol.Arguments{RoomID: "room", IncludeStartupPlan: true}, "room")
	if err != nil || !strings.Contains(string(raw), `"scope":"all_configured_placements_on_each_node"`) || !strings.Contains(string(raw), "TARGET_MEMORY_CRITICAL") || !strings.Contains(string(raw), `"projectedShards":4`) {
		t.Fatal("explicit planning evidence is unavailable", string(raw), err)
	}
}

func TestAIRoomStatusRetainsRealRuntimeAndObservationProblems(t *testing.T) {
	for _, scenario := range []string{"running_without_telemetry", "offline_unknown", "save_failure", "placement_problem", "module_failure"} {
		t.Run(scenario, func(t *testing.T) {
			source := newAIOverviewFixture()
			evidence := ""
			switch scenario {
			case "running_without_telemetry":
				delete(source.health, "master")
				evidence = "RUNTIME_ARTIFACT_MISSING"
			case "offline_unknown":
				source.failures = map[string]error{"master": &topology.ExecutionError{Code: "RUNTIME_TARGET_OFFLINE", Message: "node offline"}}
				source.topology.Targets[0].Online = false
				evidence = "RUNTIME_TARGET_OFFLINE"
			case "save_failure":
				source.statuses["master"] = shared.ShardRuntimeStatus{State: "running", Code: "SAVE_WRITE_FAILED", Message: "save failed"}
				evidence = "SAVE_WRITE_FAILED"
			case "placement_problem":
				source.topology.Placements[1].State = topology.PlacementTargetOffline
				source.topology.Issues = append(source.topology.Issues, topology.Issue{Code: "PLACEMENT_TARGET_OFFLINE", Severity: topology.SeverityError, WorldID: "caves"})
				evidence = "PLACEMENT_TARGET_OFFLINE"
			case "module_failure":
				health := source.health["master"]
				message := "sampling failed"
				health.Modules = map[string]dstruntime.ModuleHealth{"worldstate": {Ready: false, LastError: &message, ConsecutiveFailures: 2}}
				source.health["master"] = health
				evidence = message
			}
			raw, err := source.backend(t).Invoke(context.Background(), "room_status", aicontrol.Arguments{RoomID: "room"}, "")
			if err != nil || !strings.Contains(string(raw), evidence) {
				t.Fatal("real problem was hidden", string(raw), err)
			}
			if scenario == "offline_unknown" && !strings.Contains(string(raw), `"state":"unknown"`) {
				t.Fatal("unknown runtime was inferred stopped", string(raw))
			}
		})
	}
}
