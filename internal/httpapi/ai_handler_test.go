package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"dont/internal/aicontrol"
	"github.com/gin-gonic/gin"
	"github.com/jinzhu/gorm"
)

type aiLargeReceiptBackend struct{}

func (aiLargeReceiptBackend) Invoke(context.Context, string, aicontrol.Arguments, string) (json.RawMessage, error) {
	return json.Marshal(map[string]string{"log": strings.Repeat("x", 48<<10)})
}

func TestAIChatStreamsLargeReceiptsWithoutDuplicatingCompletion(t *testing.T) {
	db, err := gorm.Open("sqlite3", filepath.Join(t.TempDir(), "ai-http.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.DB().SetMaxOpenConns(1)
	db.LogMode(false)
	store := aicontrol.NewStore(db, "http_ai_", t.TempDir())
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	var count atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) == 1 {
			calls := []any{}
			for i := 0; i < 8; i++ {
				calls = append(calls, map[string]any{"id": fmt.Sprint("log-", i), "type": "function", "function": map[string]string{"name": "world_logs", "arguments": `{"roomId":"room-a","worldId":"master","limit":1}`}})
			}
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"tool_calls": calls}}}})
		} else {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"Read the receipts."}}]}`)
		}
	}))
	defer model.Close()
	if _, err := store.SaveConfig(aicontrol.ConfigInput{Config: aicontrol.Config{Revision: "initial", Enabled: true, Endpoint: model.URL, Model: "test"}}); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	NewAIHandler(aicontrol.New(store, aiLargeReceiptBackend{})).Register(router.Group("/api/v2"))
	response := performJSON(router, "POST", "/api/v2/system/ai/chat", map[string]any{"roomId": "room-a", "messages": []map[string]string{{"role": "user", "content": "Read logs"}}}, nil, "")
	if response.Code != 200 || !strings.Contains(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatal(response.Code, response.Body)
	}
	actual := 0
	complete := false
	for _, frame := range strings.Split(response.Body.String(), "\n\n") {
		if !strings.HasPrefix(frame, "data: ") {
			continue
		}
		if len(frame) > 128<<10 {
			t.Fatal("oversized SSE frame", len(frame))
		}
		var event aicontrol.ChatEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(frame, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "error" {
			t.Fatal(event.Error)
		}
		if event.Type == "result" {
			actual++
			if len(event.Call.Data) < 40<<10 {
				t.Fatal("real receipt was omitted")
			}
		}
		if event.Type == "complete" {
			complete = true
			for _, call := range event.Result.Calls {
				if len(call.Data) > 0 {
					t.Fatal("completion duplicated a full receipt")
				}
			}
		}
	}
	if actual != 8 || !complete {
		t.Fatal("incomplete stream", actual, complete)
	}
	history := performJSON(router, "GET", "/api/v2/system/ai/calls", nil, nil, "")
	if history.Body.Len() > 16<<10 {
		t.Fatal("history eagerly returned full results", history.Body.Len())
	}
}
