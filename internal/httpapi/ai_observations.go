package httpapi

import (
	"context"

	"dont/internal/runtimeoverview"
	"dont/internal/topology"
)

// AI does not hold a page subscription. After a Controller restart it may be
// the first consumer of a node's inventory. Refresh only missing/stale endpoints
// used by this room, through the existing coordinator's bounded/coalesced read.
func (b *AIBackend) refreshAIObservation(ctx context.Context, value runtimeoverview.Snapshot, includeStartupPlan bool) bool {
	if b.overview.observations == nil {
		return false
	}
	stale := map[string]bool{}
	for _, target := range value.Targets {
		stale[target.ID] = target.Online && target.Configured && (!target.InventoryAvailable || target.InventoryStale)
	}
	refreshed := false
	seen := map[string]bool{}
	for _, shard := range value.Shards {
		endpoints := [][2]string{{shard.Placement.AppliedTargetID, shard.Placement.AppliedInstallationID}}
		if includeStartupPlan {
			endpoints = append(endpoints, [2]string{shard.Placement.DesiredTargetID, shard.Placement.DesiredInstallationID})
		}
		for _, endpoint := range endpoints {
			key := endpoint[0] + "\x00" + endpoint[1]
			if !stale[endpoint[0]] || endpoint[0] == "" || endpoint[1] == "" || seen[key] {
				continue
			}
			seen[key] = true
			// Failed collection stays unavailable in the coordinator; rereading
			// the overview retains partial state instead of claiming success.
			_ = b.overview.observations.RefreshRuntimeTarget(ctx, endpoint[0], endpoint[1])
			refreshed = true
		}
	}
	return refreshed
}

// Present current evidence to AI clients without mixing in absent historical
// reports or hypothetical fleet capacity. The full UI overview stays unchanged.
func aiRoomStatus(value runtimeoverview.Snapshot, includeStartupPlan bool) map[string]any {
	shards := make([]any, 0, len(value.Shards))
	for _, shard := range value.Shards {
		telemetry := map[string]any{"status": "not_expected", "reason": "world_stopped"}
		if shard.Runtime.State != "stopped" || shard.RuntimeProblem != nil {
			telemetry = map[string]any{
				"status": shard.Health.Freshness, "available": shard.Health.Available,
				"observedAt": shard.Health.ObservedAt, "ageSeconds": shard.Health.AgeSeconds,
			}
			if shard.Health.Problem != nil {
				telemetry["problem"] = shard.Health.Problem
			}
			if health := shard.Health.Health; health != nil {
				telemetry["ready"] = health.Ready
				telemetry["running"] = health.Running
				telemetry["consecutiveFailures"] = health.ConsecutiveFailures
				telemetry["lastError"] = health.LastError
				telemetry["modules"] = health.Modules
			}
		}
		item := map[string]any{
			"worldId": shard.WorldID, "worldName": shard.WorldName, "worldRole": shard.WorldRole,
			"state": shard.State, "runtime": shard.Runtime, "placement": shard.Placement,
			"telemetry": telemetry,
		}
		if shard.RuntimeProblem != nil {
			item["runtimeProblem"] = shard.RuntimeProblem
		}
		shards = append(shards, item)
	}
	targets := make([]any, 0, len(value.Targets))
	planned := make([]any, 0, len(value.Targets))
	for _, target := range value.Targets {
		capacity := target.CurrentCapacity
		targets = append(targets, map[string]any{
			"id": target.ID, "name": target.Name, "online": target.Online, "configured": target.Configured,
			"inventoryAvailable": target.InventoryAvailable, "inventoryStale": target.InventoryStale,
			"observedAt": target.ObservedAt, "observationState": target.ObservationState,
			"observationError": target.ObservationError, "staleReason": target.StaleReason,
			"observedRunningShards": target.ObservedRunningShards,
			"currentCapacity": map[string]any{
				"state": capacity.State, "runningShards": capacity.RunningShards,
				"logicalProcessors": capacity.LogicalProcessors, "physicalCores": capacity.PhysicalCores,
				"recommendedShardLimit": capacity.RecommendedShardLimit,
				"memoryState":           capacity.MemoryState, "memoryTotalBytes": capacity.MemoryTotalBytes,
				"memoryAvailableBytes": capacity.MemoryAvailableBytes,
			},
		})
		if includeStartupPlan {
			planned = append(planned, map[string]any{
				"targetId": target.ID, "targetName": target.Name,
				"plannedShards": target.PlannedShards, "projectedShards": target.ProjectedShards,
				"projectedCapacity":              target.ProjectedCapacity,
				"requiresOvercommitConfirmation": target.RequiresOvercommitConfirmation,
			})
		}
	}
	issues, plannedIssues := []topology.Issue{}, []topology.Issue{}
	for _, issue := range value.Issues {
		switch issue.Code {
		case "TARGET_OVERCOMMITTED", "TARGET_CAPACITY_FULL", "TARGET_CAPACITY_UNKNOWN", "TARGET_MEMORY_CRITICAL", "TARGET_MEMORY_TIGHT":
			plannedIssues = append(plannedIssues, issue)
		default:
			issues = append(issues, issue)
		}
	}
	result := map[string]any{
		"roomId": value.RoomID, "observedAt": value.ObservedAt, "summary": value.Summary,
		"shards": shards, "targets": targets, "issues": issues,
	}
	if includeStartupPlan {
		result["startupPlan"] = map[string]any{
			"scope":   "all_configured_placements_on_each_node",
			"meaning": "Hypothetical capacity if all configured placements run, including other rooms. This is not current usage, evidence of a past failure, or a preview of starting only this room. Actual start actions perform their own capacity check.",
			"targets": planned, "issues": plannedIssues,
		}
	}
	return result
}
