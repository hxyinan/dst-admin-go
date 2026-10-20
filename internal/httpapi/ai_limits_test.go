package httpapi

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"dont/internal/aicontrol"
	"github.com/gin-gonic/gin"
	"github.com/jinzhu/gorm"
)

func TestAIConfigAPISavesCustomAndUnlimitedLimits(t *testing.T) {
	db, err := gorm.Open("sqlite3", filepath.Join(t.TempDir(), "limits.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.DB().SetMaxOpenConns(1)
	db.LogMode(false)
	store := aicontrol.NewStore(db, "limits_", t.TempDir())
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	NewAIHandler(aicontrol.New(store, aiLargeReceiptBackend{})).Register(router.Group("/api/v2"))
	revision := "initial"
	for _, limits := range []map[string]int{{"maxRounds": 25, "maxToolCalls": 80, "timeoutSeconds": 600}, {"maxRounds": 0, "maxToolCalls": 0, "timeoutSeconds": 0}} {
		response := performJSON(router, http.MethodPut, "/api/v2/system/ai", map[string]any{"revision": revision, "enabled": false, "endpoint": "", "model": "", "limits": limits}, nil, "")
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body)
		}
		var saved struct{ Data aicontrol.Config }
		if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
			t.Fatal(err)
		}
		revision = saved.Data.Revision
		if saved.Data.Limits.MaxRounds != limits["maxRounds"] || saved.Data.Limits.MaxToolCalls != limits["maxToolCalls"] || saved.Data.Limits.TimeoutSeconds != limits["timeoutSeconds"] {
			t.Fatal("limits not returned", saved)
		}
		response = performJSON(router, http.MethodGet, "/api/v2/system/ai", nil, nil, "")
		var read struct {
			Data struct{ Config aicontrol.Config }
		}
		if err := json.Unmarshal(response.Body.Bytes(), &read); err != nil {
			t.Fatal(err)
		}
		if read.Data.Config.Limits == nil || *read.Data.Config.Limits != *saved.Data.Limits {
			t.Fatal("limits not persisted", read, saved)
		}
	}
	for _, invalid := range []map[string]any{{"maxRounds": -1}, {"maxToolCalls": 1.5}, {"timeoutSeconds": -1}, {"timeoutSeconds": 9223372037}} {
		response := performJSON(router, http.MethodPut, "/api/v2/system/ai", map[string]any{"revision": revision, "enabled": false, "endpoint": "", "model": "", "limits": invalid}, nil, "")
		if response.Code != 422 {
			t.Fatal("invalid limits accepted", invalid, response.Code, response.Body)
		}
	}
}
