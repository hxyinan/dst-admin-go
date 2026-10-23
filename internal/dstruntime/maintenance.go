package dstruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dont/internal/rooms"
	"dont/internal/runtimefiles"
	"dont/internal/shards"
	"dont/shared"

	"github.com/google/uuid"
)

// MaintenanceControl is node-local. The caller owns the room operation lease;
// neither the controller nor this component writes another node's save paths.
type MaintenanceControl interface {
	Status(context.Context, string, string) (shards.RuntimeStatus, error)
	Send(context.Context, string, string, string) error
}

type maintenanceCatalog struct {
	room  rooms.Room
	world rooms.World
}

func (c maintenanceCatalog) Room(string) (rooms.Room, error)           { return c.room, nil }
func (c maintenanceCatalog) World(string, string) (rooms.World, error) { return c.world, nil }
func (c maintenanceCatalog) Worlds(string) ([]rooms.World, error)      { return []rooms.World{c.world}, nil }

type maintenanceProcess struct{ control MaintenanceControl }

func (p maintenanceProcess) IsRunning(ctx context.Context, room, world string) (bool, error) {
	status, err := p.control.Status(ctx, room, world)
	if err != nil {
		return false, err
	}
	switch status.State {
	case shards.RuntimeRunning:
		return true, nil
	case shards.RuntimeStopped, shards.RuntimeFailed:
		return false, nil
	default:
		return false, fmt.Errorf("%w: Runtime 正在切换状态（%s），稍后重试更新", ErrRuntimeRefreshDeferred, status.State)
	}
}

var maintenanceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// EnsureNodeWorld installs the node's embedded version, then verifies the live
// instance. Automatic calls only maintain existing opted-in installations.
func EnsureNodeWorld(ctx context.Context, root, cluster, shard string, request shared.RuntimeLuaRequest, control MaintenanceControl) (shared.RuntimeLuaResult, error) {
	var result shared.RuntimeLuaResult
	if request.Version != RuntimeVersion {
		return result, fmt.Errorf("节点内置 Runtime 为 %s，控制端要求 %s；请先升级节点程序", RuntimeVersion, request.Version)
	}
	if control == nil || !maintenanceName.MatchString(cluster) || !maintenanceName.MatchString(shard) {
		return result, ErrUnsafeRuntimePath
	}
	for _, path := range []string{filepath.Join(root, cluster), filepath.Join(root, cluster, shard)} {
		info, err := os.Lstat(path)
		if err != nil {
			return result, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return result, ErrUnsafeRuntimePath
		}
	}
	catalog := maintenanceCatalog{room: rooms.Room{ID: rooms.EncodeID(cluster), DirectoryName: cluster, Managed: true}, world: rooms.World{ID: rooms.EncodeID(shard), DirectoryName: shard, Name: shard}}
	manager, err := NewManager(root, catalog)
	if err != nil {
		return result, err
	}
	roomID, worldID := catalog.room.ID, catalog.world.ID
	status := manager.inspect(catalog.room, catalog.world)
	result.State, result.Version = string(status.State), status.Version
	if status.State == InstallStateMissing && !request.InstallIfMissing {
		result.Message = "Runtime 未启用，保留当前设置"
		return result, nil
	}
	process := maintenanceProcess{control}
	running, err := process.IsRunning(ctx, cluster, shard)
	if err != nil {
		result.Deferred = errors.Is(err, ErrRuntimeRefreshDeferred)
		return result, err
	}
	result.Running = running
	if running {
		if health, err := manager.Health(roomID, worldID); err == nil && maintenanceHealthMatchesProcess(ctx, root, cluster, shard, health, control) {
			for _, name := range []string{"commands", "barriers", "diagnostics"} {
				module := health.Modules[name]
				if module.Busy || module.Holding || module.Pending > 0 {
					result.Deferred = true
					return result, fmt.Errorf("%w: Runtime 正在执行 %s 操作，更新已暂缓", ErrRuntimeRefreshDeferred, name)
				}
			}
		}
	}
	installed, err := manager.InstallWorld(ctx, roomID, worldID)
	if err != nil {
		return result, err
	}
	result.State, result.Version, result.Changed = string(installed.State), installed.Version, installed.Changed
	if !running {
		result.Message = "Runtime 文件已就绪，下次启动世界自动加载"
		return result, nil
	}
	bridge, _ := NewBridge(manager, process, control)
	worldPath := filepath.Join(root, cluster, shard)
	if !installed.Changed {
		if health, err := manager.Health(roomID, worldID); err == nil && maintenanceHealthCurrent(ctx, root, cluster, shard, health, bridge, control) {
			result.Active, result.Message = true, "Runtime 已是最新版本并正常运行"
			return result, nil
		}
	}
	// A token lets timeout recovery cancel an asynchronous bootstrap before it
	// can replace the restored instance. Keep the stopped previous instance until
	// the node observes fresh health and both coherent snapshots.
	token := uuid.NewString()
	start := time.Now().UTC()
	script := `local __update={id=` + strconv.Quote(token) + `,previous=rawget(_G,"DSTAdmin")};rawset(_G,"DSTAdminPendingUpdate",__update);` + strings.Replace(managedActivationScript, `function(ok,source) `, `function(ok,source) if rawget(_G,"DSTAdminPendingUpdate")~=__update or __update.aborted then return end `, 1)
	err = control.Send(ctx, cluster, shard, script)
	if err == nil {
		var health Health
		health, err = bridge.waitForLifecycleHealth(ctx, roomID, worldID, worldPath, start, "", token)
		if err == nil {
			players, playersErr := manager.ReadPlayers(ctx, roomID, worldID)
			world, worldErr := manager.ReadWorldState(ctx, roomID, worldID)
			err = errors.Join(playersErr, worldErr)
			if err == nil && !refreshOutputsMatch(health, players, world, health.SessionID, health.ShardID) {
				err = ErrRuntimeActivation
			}
		}
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err == nil {
		err = control.Send(cleanup, cluster, shard, `local u=rawget(_G,"DSTAdminPendingUpdate"); if u and u.id==`+strconv.Quote(token)+` then rawset(_G,"DSTAdminPendingUpdate",nil) end`)
		if err == nil {
			result.Active, result.Message = true, "Runtime 已自动更新并确认采集正常，世界未重启"
			return result, nil
		}
	}
	// Abort the matching attempt only; never stop an unrelated/newer instance.
	abort := `local u=rawget(_G,"DSTAdminPendingUpdate"); if u and u.id==` + strconv.Quote(token) + ` then u.aborted=true; if u.candidate and rawget(_G,"DSTAdmin")==u.candidate then pcall(u.candidate.Stop); rawset(_G,"DSTAdmin",u.previous); if u.previous then pcall(u.previous.Start); if u.previous.Refresh then pcall(u.previous.Refresh) end end end; rawset(_G,"DSTAdminPendingUpdate",nil) end`
	abortErr := control.Send(cleanup, cluster, shard, abort)
	var restoreErr error
	if installed.Changed {
		restoreErr = manager.restoreStateBackup(worldPath, filepath.Base(installed.BackupPath))
	}
	restored := manager.inspect(catalog.room, catalog.world)
	result.State, result.Version = string(restored.State), restored.Version
	result.Active, result.Message = false, "Runtime 更新未确认成功，已尝试恢复更新前状态"
	return result, errors.Join(err, abortErr, restoreErr)
}

// Paused worlds do not run periodic collection. Their existing health is still
// current when its timestamps belong to the process identified by this log.
func maintenanceHealthCurrent(ctx context.Context, root, cluster, shard string, health Health, bridge *Bridge, control MaintenanceControl) bool {
	worldPath := filepath.Join(root, cluster, shard)
	if !maintenanceHealthMatchesProcess(ctx, root, cluster, shard, health, control) {
		return false
	}
	if bridge.validLifecycleHealth(worldPath, health, time.Time{}) {
		return true
	}
	status, err := control.Status(ctx, cluster, shard)
	if err != nil || status.Paused == nil || !*status.Paused {
		return false
	}
	value, err := runtimefiles.ReadWorldState(ctx, root, cluster, shard, shared.ShardRuntimeStatus{State: string(status.State), Paused: status.Paused})
	if err != nil || value.ReadError != "" || !pausedHealthMatchesCurrentProcess(bridge.now().UTC(), health, value) {
		return false
	}
	// All readiness, module and version checks still apply; only age is waived.
	health.ReadAt = bridge.now().UTC()
	return bridge.validLifecycleHealth(worldPath, health, time.Time{})
}

func maintenanceHealthMatchesProcess(ctx context.Context, root, cluster, shard string, health Health, control MaintenanceControl) bool {
	status, err := control.Status(ctx, cluster, shard)
	if err != nil {
		return false
	}
	value, err := runtimefiles.ReadWorldState(ctx, root, cluster, shard, shared.ShardRuntimeStatus{State: string(status.State), Paused: status.Paused})
	return err == nil && value.ReadError == "" && healthMatchesCurrentProcess(time.Now().UTC(), health, value)
}
