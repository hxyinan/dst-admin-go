package aicontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestChatLimitsLegacyDefaultsAndSaveCompatibility(t *testing.T) {
	_, store := fixture(t, nil)
	if err := store.db.Table(store.configTable).Where("id = ?", 1).UpdateColumn("data", `{"revision":"legacy","enabled":true,"endpoint":"http://example.com","model":"test"}`).Error; err != nil {
		t.Fatal(err)
	}
	config, err := store.Config()
	if err != nil || config.Limits == nil || *config.Limits != defaultChatLimits() {
		t.Fatal("legacy limits", config, err)
	}
	config.Limits = &ChatLimits{MaxRounds: 40, MaxToolCalls: 80, TimeoutSeconds: 900}
	saved, err := store.SaveConfig(ConfigInput{Config: config})
	if err != nil {
		t.Fatal(err)
	}
	oldClient := saved
	oldClient.Limits = nil
	oldClient.Model = "changed"
	saved, err = store.SaveConfig(ConfigInput{Config: oldClient})
	if err != nil {
		t.Fatal(err)
	}
	if *saved.Limits != *config.Limits {
		t.Fatal("old client reset saved limits", saved.Limits)
	}
	saved.Limits = &ChatLimits{}
	saved, err = store.SaveConfig(ConfigInput{Config: saved})
	if err != nil {
		t.Fatal(err)
	}
	reread, err := store.Config()
	if err != nil || *reread.Limits != (ChatLimits{}) {
		t.Fatal("zero limits lost", reread, err)
	}
	provider, _, err := store.Provider()
	if err != nil || *provider.Limits != (ChatLimits{}) {
		t.Fatal("provider lost zero limits", provider, err)
	}
}

func TestChatLimitsJSONDefaultsAndValidation(t *testing.T) {
	for _, tc := range []struct {
		json string
		want ChatLimits
	}{
		{`{}`, defaultChatLimits()},
		{`{"maxRounds":30}`, ChatLimits{30, 12, 150}},
		{`{"maxRounds":0,"maxToolCalls":0,"timeoutSeconds":0}`, ChatLimits{}},
	} {
		var limits ChatLimits
		if err := json.Unmarshal([]byte(tc.json), &limits); err != nil || limits != tc.want {
			t.Fatal(limits, err, tc.want)
		}
	}
	for _, limits := range []ChatLimits{{-1, 12, 150}, {8, -1, 150}, {8, 12, -1}, {8, 12, 9223372037}} {
		if validateConfig(ConfigInput{Config: Config{Limits: &limits}}) == nil {
			t.Fatal("invalid limits accepted", limits)
		}
	}
	for _, limits := range []ChatLimits{{}, {5000, 10000, 86400}, {8, 12, 9223372036}} {
		if err := validateConfig(ConfigInput{Config: Config{Limits: &limits}}); err != nil {
			t.Fatal("administrator setting rejected", limits, err)
		}
	}
}

func TestChatUsesConfiguredRoundsCallsAndUnlimitedModes(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		limits                      ChatLimits
		batch, available, wantCalls int
		limited                     bool
	}{
		{"custom round limit", ChatLimits{2, 0, 150}, 1, 9, 2, true},
		{"custom call limit trims a batch", ChatLimits{0, 3, 150}, 8, 9, 3, true},
		{"larger batch and call allowance", ChatLimits{0, 16, 150}, 16, 20, 16, true},
		{"unlimited rounds and calls exceed previous ceilings", ChatLimits{}, 2, 9, 18, false},
		{"raised rounds exceed default", ChatLimits{10, 30, 150}, 2, 9, 18, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executions, requests, decisions, summaries := 0, 0, 0, 0
			s, store := fixture(t, backendFunc(func(ctx context.Context, _ string, _ Arguments, _ string) (json.RawMessage, error) {
				executions++
				deadline, hasDeadline := ctx.Deadline()
				if tc.limits.TimeoutSeconds == 0 {
					// Each platform operation retains its own transport deadline;
					// disabling the whole turn does not remove per-tool safeguards.
					if !hasDeadline || time.Until(deadline) > 30*time.Second {
						t.Error("tool deadline changed", deadline)
					}
				} else if !hasDeadline || time.Until(deadline) > time.Duration(tc.limits.TimeoutSeconds)*time.Second {
					t.Error("configured timeout not applied", deadline)
				}
				return json.RawMessage(`{"runtimeState":"running"}`), nil
			}))
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
					fmt.Fprint(w, `{"choices":[{"message":{"content":"Summary of collected findings."}}]}`)
					return
				}
				decisions++
				if decisions > tc.available {
					fmt.Fprint(w, `{"choices":[{"message":{"content":"All requested checks complete."}}]}`)
					return
				}
				calls := []any{}
				for i := 0; i < tc.batch; i++ {
					calls = append(calls, map[string]any{"id": fmt.Sprintf("read-%d-%d", requests, i), "type": "function", "function": map[string]string{"name": "room_status", "arguments": `{"roomId":"room-a"}`}})
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"tool_calls": calls}}}})
			}))
			defer provider.Close()
			config, _ := store.Config()
			config.Endpoint, config.Model = provider.URL, "test"
			config.Limits = &tc.limits
			if _, err := store.SaveConfig(ConfigInput{Config: config}); err != nil {
				t.Fatal(err)
			}
			out, err := s.Chat(context.Background(), ChatInput{RoomID: "room-a", Messages: []Message{{Role: "user", Content: "check"}}}, func(ChatEvent) {})
			if err != nil || out.Limited != tc.limited || len(out.Calls) != tc.wantCalls || executions != tc.wantCalls || out.Content == "" {
				t.Fatalf("limits ignored: %+v %v executions=%d requests=%d", out, err, executions, requests)
			}
			if tc.limited && summaries != 1 {
				t.Error("limited turn missing final summary")
			}
		})
	}
}

func TestUnlimitedChatContextStillObeysRequestCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	child, stop := chatContext(parent, ChatLimits{})
	defer stop()
	if _, ok := child.Deadline(); ok {
		t.Fatal("unlimited turn has a deadline")
	}
	cancel()
	select {
	case <-child.Done():
	case <-time.After(time.Second):
		t.Fatal("parent cancellation ignored")
	}
	short, stop := chatContext(context.Background(), ChatLimits{TimeoutSeconds: 1})
	defer stop()
	deadline, ok := short.Deadline()
	if !ok || time.Until(deadline) > time.Second || time.Until(deadline) < 900*time.Millisecond {
		t.Fatal("timeout not applied", deadline)
	}
}

func TestUnlimitedBatchesStillBoundContextAndKeepToolMessagesPaired(t *testing.T) {
	s, store := fixture(t, nil)
	callsRun := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools    []any     `json:"tools"`
			Messages []Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Tools) == 0 {
			ids := map[string]bool{}
			results := 0
			for _, message := range body.Messages {
				for _, call := range message.ToolCalls {
					ids[call.ID] = true
				}
				if message.Role == "tool" {
					results++
					if !ids[message.ToolCallID] {
						t.Error("orphan tool result")
					}
				}
			}
			if len(ids) != results || results != callsRun {
				t.Error("unexecuted calls left in summary", len(ids), results, callsRun)
			}
			raw, _ := json.Marshal(body.Messages)
			if len(raw) > 128<<10 {
				t.Error("summary context grew without bound", len(raw))
			}
			fmt.Fprint(w, `{"choices":[{"message":{"content":"Partial findings retained."}}]}`)
			return
		}
		calls := []any{}
		for i := 0; i < 1000; i++ {
			calls = append(calls, map[string]any{"id": fmt.Sprint("large-", i), "type": "function", "function": map[string]string{"name": "room_status", "arguments": `{"roomId":"room-a"}`}})
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"tool_calls": calls}}}})
	}))
	defer provider.Close()
	s.backend = backendFunc(func(context.Context, string, Arguments, string) (json.RawMessage, error) {
		callsRun++
		return json.RawMessage(`{"state":"running"}`), nil
	})
	config, _ := store.Config()
	config.Endpoint, config.Model = provider.URL, "test"
	config.Limits = &ChatLimits{}
	if _, err := store.SaveConfig(ConfigInput{Config: config}); err != nil {
		t.Fatal(err)
	}
	out, err := s.Chat(context.Background(), ChatInput{RoomID: "room-a", Messages: []Message{{Role: "user", Content: "check"}}}, func(ChatEvent) {})
	if err != nil || !out.Limited || callsRun == 0 || callsRun >= 1000 || len(out.Calls) != callsRun {
		t.Fatal(out, err, callsRun)
	}
}
