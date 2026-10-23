package dstruntime

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"dont/internal/operationlease"
	"dont/internal/rooms"
	"dont/shared"
)

type MaintenanceCatalog interface {
	Catalog
	List() ([]rooms.Room, error)
}
type MaintenanceRuntime interface {
	EnsureLuaRuntime(context.Context, string, string, shared.RuntimeLuaRequest) (shared.RuntimeLuaResult, error)
}

type UpdateReport struct {
	State             string    `json:"state"`
	Message           string    `json:"message"`
	Version           string    `json:"version,omitempty"`
	InstallationState string    `json:"installationState,omitempty"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// AutoUpdater has no polling loop. Startup, node generation/capability changes,
// and explicit room adoption wake a single bounded maintenance worker.
type AutoUpdater struct {
	catalog MaintenanceCatalog
	runtime MaintenanceRuntime
	wake    chan struct{}
	mu      sync.RWMutex
	reports map[string]UpdateReport
}

func NewAutoUpdater(catalog MaintenanceCatalog, runtime MaintenanceRuntime) *AutoUpdater {
	return &AutoUpdater{catalog: catalog, runtime: runtime, wake: make(chan struct{}, 1), reports: make(map[string]UpdateReport)}
}

func (u *AutoUpdater) Wake() {
	select {
	case u.wake <- struct{}{}:
	default:
	}
}
func (u *AutoUpdater) Report(roomID, worldID string) (UpdateReport, bool) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	value, ok := u.reports[roomID+"\x00"+worldID]
	return value, ok
}
func (u *AutoUpdater) record(roomID, worldID string, value UpdateReport) {
	u.mu.Lock()
	defer u.mu.Unlock()
	key := roomID + "\x00" + worldID
	if len(u.reports) >= 1024 { // Reporting is bounded; maintenance still covers every world.
		if _, exists := u.reports[key]; !exists {
			for old := range u.reports {
				delete(u.reports, old)
				break
			}
		}
	}
	u.reports[key] = value
}

func (u *AutoUpdater) EnsureWorld(ctx context.Context, roomID, worldID string, install bool) (shared.RuntimeLuaResult, error) {
	room, err := u.catalog.Room(roomID)
	if err != nil {
		return shared.RuntimeLuaResult{}, err
	}
	if !room.Managed {
		return shared.RuntimeLuaResult{}, rooms.ErrRoomNotManaged
	}
	if _, err := u.catalog.World(room.ID, worldID); err != nil {
		return shared.RuntimeLuaResult{}, err
	}
	u.record(roomID, worldID, UpdateReport{State: "updating", Message: "正在检查并更新 Runtime", UpdatedAt: time.Now().UTC()})
	value, err := u.runtime.EnsureLuaRuntime(ctx, roomID, worldID, shared.RuntimeLuaRequest{Version: RuntimeVersion, InstallIfMissing: install})
	report := UpdateReport{State: "current", Message: value.Message, Version: value.Version, InstallationState: value.State, UpdatedAt: time.Now().UTC()}
	if value.Changed {
		report.State = "updated"
	}
	if value.State == string(InstallStateMissing) {
		report.State = "disabled"
	}
	if err != nil {
		report.State, report.Message = "failed", err.Error()
	}
	u.record(roomID, worldID, report)
	return value, err
}

func (u *AutoUpdater) Run(ctx context.Context) {
	type target struct {
		roomID, worldID string
		attempts        int
		next            time.Time
	}
	pending := make(map[string]target)
	u.Wake()
	for {
		var selected target
		var selectedKey string
		for key, value := range pending {
			if selectedKey == "" || value.next.Before(selected.next) {
				selectedKey, selected = key, value
			}
		}
		var timer *time.Timer
		var ready <-chan time.Time
		if selectedKey != "" {
			timer = time.NewTimer(max(time.Until(selected.next), 0))
			ready = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-u.wake:
			if timer != nil {
				timer.Stop()
			}
			items, err := u.catalog.List()
			if err != nil {
				log.Printf("[RuntimeUpdate] list rooms: %v", err)
				continue
			}
			for _, room := range items {
				if !room.Managed {
					continue
				}
				worlds, err := u.catalog.Worlds(room.ID)
				if err != nil {
					log.Printf("[RuntimeUpdate] list worlds: %v", err)
					continue
				}
				for _, world := range worlds {
					key := room.ID + "\x00" + world.ID
					if _, exists := pending[key]; !exists {
						pending[key] = target{roomID: room.ID, worldID: world.ID, next: time.Now()}
					}
				}
			}
		case <-ready:
			delete(pending, selectedKey)
			attemptCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
			_, err := u.EnsureWorld(attemptCtx, selected.roomID, selected.worldID, false)
			cancel()
			if err == nil {
				continue
			}
			if selected.attempts < 8 && (errors.Is(err, operationlease.ErrBusy) || errors.Is(err, ErrRuntimeRefreshDeferred)) {
				delay := min(time.Second*time.Duration(1<<selected.attempts), 30*time.Second)
				selected.attempts++
				selected.next = time.Now().Add(delay)
				pending[selectedKey] = selected
				report, _ := u.Report(selected.roomID, selected.worldID)
				report.State, report.Message = "updating", "等待当前操作结束后自动继续更新 Runtime"
				u.record(selected.roomID, selected.worldID, report)
				continue
			}
			log.Printf("[RuntimeUpdate] room=%s world=%s: %v", selected.roomID, selected.worldID, err)
		}
	}
}
