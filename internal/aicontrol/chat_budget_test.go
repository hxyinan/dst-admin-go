package aicontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatChecksTwoRoomsWithoutStoppingBeforeItsFindings(t *testing.T) {
	s, store := fixture(t, nil)
	steps := []struct{ name, args string }{
		{"list_rooms", `{}`},
		{"room_status", `{"roomId":"room-a"}`},
		{"room_status", `{"roomId":"room-b"}`},
		{"world_state", `{"roomId":"room-a","worldId":"master"}`},
		{"world_state", `{"roomId":"room-a","worldId":"caves"}`},
		{"world_state", `{"roomId":"room-b","worldId":"master"}`},
		{"world_state", `{"roomId":"room-b","worldId":"caves"}`},
	}
	requests := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests > len(steps) {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"room-a 正常运行且采集新鲜；room-b 已停止，当前没有运行数据。"}}]}`)
			return
		}
		step := steps[requests-1]
		calls := []any{map[string]any{"id": fmt.Sprint("step-", requests), "type": "function", "function": map[string]string{"name": step.name, "arguments": step.args}}}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"tool_calls": calls}}}})
	}))
	defer provider.Close()
	config, _ := store.Config()
	config.Endpoint, config.Model = provider.URL, "test"
	if _, err := store.SaveConfig(ConfigInput{Config: config}); err != nil {
		t.Fatal(err)
	}
	out, err := s.Chat(context.Background(), ChatInput{Messages: []Message{{Role: "user", Content: "都检查一下"}}}, func(ChatEvent) {})
	if err != nil || out.Limited || len(out.Calls) != 7 || !strings.Contains(out.Content, "room-b 已停止") || requests != 8 {
		t.Fatalf("incomplete multi-room findings: %+v %v requests=%d", out, err, requests)
	}
}

func TestChatAlwaysReservesAToolFreeSummaryAndRetainsPartialEvidence(t *testing.T) {
	for _, failedSummary := range []bool{false, true} {
		t.Run(fmt.Sprint("providerFailure=", failedSummary), func(t *testing.T) {
			s, store := fixture(t, nil)
			requests, summaries := 0, 0
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Tools    []any     `json:"tools"`
					Messages []Message `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				requests++
				if len(body.Tools) == 0 {
					summaries++
					receipts := 0
					for _, m := range body.Messages {
						if m.Role == "tool" {
							receipts++
						}
					}
					if receipts != 8 || body.Messages[len(body.Messages)-1].Role != "system" {
						t.Error("summary lost actual receipts", receipts)
					}
					if failedSummary {
						w.WriteHeader(503)
						return
					}
					fmt.Fprint(w, `{"choices":[{"message":{"content":"已检查部分世界，剩余世界尚未核实。"}}]}`)
					return
				}
				fmt.Fprintf(w, `{"choices":[{"message":{"tool_calls":[{"id":"read-%d","type":"function","function":{"name":"room_status","arguments":"{\"roomId\":\"room-a\"}"}}]}}]}`, requests)
			}))
			defer provider.Close()
			config, _ := store.Config()
			config.Endpoint, config.Model = provider.URL, "test"
			if _, err := store.SaveConfig(ConfigInput{Config: config}); err != nil {
				t.Fatal(err)
			}
			var events []ChatEvent
			out, err := s.Chat(context.Background(), ChatInput{RoomID: "room-a", Messages: []Message{{Role: "user", Content: "检查状态"}}}, func(event ChatEvent) { events = append(events, event) })
			wantRequests, wantSummaries := 9, 1
			if failedSummary {
				wantRequests, wantSummaries = 10, 2 // One retry of the model summary, no tool replay.
			}
			if err != nil || !out.Limited || len(out.Calls) != 8 || requests != wantRequests || summaries != wantSummaries || out.Content == "" {
				t.Fatalf("summary missing: %+v %v requests=%d", out, err, requests)
			}
			if events[len(events)-1].Type != "summarizing" {
				t.Error("missing summarizing phase")
			}
			if failedSummary && !strings.Contains(out.Content, "AI_PROVIDER_HTTP_503") {
				t.Error("provider failure hidden", out.Content)
			}
		})
	}
}
