package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"dont/internal/dstruntime"
	"dont/internal/rooms"
	"dont/shared"
	"github.com/gin-gonic/gin"
)

type handlerRuntimeUpdater struct{ worlds []string }

func (u *handlerRuntimeUpdater) EnsureWorld(_ context.Context, _, world string, _ bool) (shared.RuntimeLuaResult, error) {
	u.worlds = append(u.worlds, world)
	return shared.RuntimeLuaResult{Version: dstruntime.RuntimeVersion, State: "installed", Changed: true, Message: "node runtime updated"}, nil
}
func (u *handlerRuntimeUpdater) Report(string, string) (dstruntime.UpdateReport, bool) {
	return dstruntime.UpdateReport{State: "updated", Version: dstruntime.RuntimeVersion, InstallationState: "installed", UpdatedAt: time.Now()}, true
}

func TestRuntimeInstallationUsesNodeUpdaterForRemoteWorld(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, manager, catalog, process := newRuntimeHandlerTestApp(t)
	updater := &handlerRuntimeUpdater{}
	bridge := &runtimeHandlerHealthBridge{local: false}
	handler := NewDSTRuntimeHandler(manager, catalog, process, bridge)
	handler.ConfigureUpdater(updater)
	router := gin.New()
	handler.Register(router.Group("/api/v2"))
	worldID := rooms.EncodeID("Master")
	response := performJSON(router, http.MethodPost, "/api/v2/rooms/"+catalog.room.ID+"/worlds/"+worldID+"/runtime/actions/install", nil, nil, "")
	assertStatus(t, response, http.StatusOK)
	if manager.installCalls != 0 || len(updater.worlds) != 1 || updater.worlds[0] != worldID {
		t.Fatalf("remote install used controller files: calls=%d worlds=%v", manager.installCalls, updater.worlds)
	}
	response = performJSON(router, http.MethodGet, "/api/v2/rooms/"+catalog.room.ID+"/runtime", nil, nil, "")
	assertStatus(t, response, http.StatusOK)
	items := responseData(t, response)["items"].([]interface{})
	for _, item := range items {
		report := item.(map[string]interface{})
		if report["state"] != "installed" || report["healthState"] != "stopped" || report["autoUpdate"] == nil {
			t.Fatalf("stopped Agent installation was reported missing: %s", response.Body.String())
		}
	}
}
