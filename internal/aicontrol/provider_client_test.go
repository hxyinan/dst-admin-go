package aicontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dont/internal/apicontract"
)

func TestProviderRetriesOnlyTransientModelRequests(t *testing.T) {
	for _, status := range []int{400, 401, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(status)
					return
				}
				fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"}}]}`)
			}))
			defer server.Close()
			message, err := complete(context.Background(), Config{Endpoint: server.URL, Model: "test"}, "private-key", []Message{{Role: "user", Content: "check"}}, nil)
			if status == 400 || status == 401 {
				if err == nil || requests.Load() != 1 {
					t.Fatal("non-transient request was retried", requests.Load(), err)
				}
			} else if err != nil || message.Content != "OK" || requests.Load() != 2 {
				t.Fatal("transient model request did not recover", message, requests.Load(), err)
			}
		})
	}
}

func TestProviderRecoversFromConnectionClosedBeforeResponse(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			connection.Close()
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"}}]}`)
	}))
	defer server.Close()
	message, err := complete(context.Background(), Config{Endpoint: server.URL, Model: "test"}, "", []Message{{Role: "user", Content: "check"}}, nil)
	if err != nil || message.Content != "OK" || requests.Load() != 2 {
		t.Fatal("connection interruption did not recover", message, err, requests.Load())
	}
}

func TestProviderDistinguishesTimeoutAndHonorsRequestCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := complete(ctx, Config{Endpoint: server.URL, Model: "test"}, "", []Message{{Role: "user", Content: "check"}}, nil)
	if err == nil || err.Error() != "AI_PROVIDER_TIMEOUT" {
		t.Fatal("timeout was hidden as an undifferentiated connection error", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	_, err = complete(ctx, Config{Endpoint: server.URL, Model: "test"}, "", []Message{{Role: "user", Content: "check"}}, nil)
	if err == nil || err.Error() != "AI_REQUEST_CANCELED" {
		t.Fatal("cancellation was hidden", err)
	}
}

func TestChatProviderFailureKeepsSubmittedJobAndNeverRepeatsMutation(t *testing.T) {
	var mutations, reads, requests atomic.Int32
	s, store := fixture(t, backendFunc(func(_ context.Context, name string, _ Arguments, _ string) (json.RawMessage, error) {
		if name == "get_job" {
			reads.Add(1)
			return json.RawMessage(`{"id":"job-backup","roomId":"room-a","kind":"backup.create","status":"failed","outcome":"none","errorMessage":"snapshot version mismatch"}`), nil
		}
		mutations.Add(1)
		return json.RawMessage(`{"id":"job-backup","roomId":"room-a","kind":"backup.create","status":"queued","outcome":"pending"}`), nil
	}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"id":"save","type":"function","function":{"name":"room_control","arguments":"{\"roomId\":\"room-a\",\"action\":\"save\",\"requestId\":\"ignored-model-id\"}"}}]}}]}`)
			return
		}
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	config, _ := store.Config()
	config.Endpoint, config.Model = server.URL, "test"
	if _, err := store.SaveConfig(ConfigInput{Config: config}); err != nil {
		t.Fatal(err)
	}
	out, err := s.Chat(context.Background(), ChatInput{Messages: []Message{{Role: "user", Content: "save"}}}, func(ChatEvent) {})
	if err != nil || mutations.Load() != 1 || reads.Load() != 1 || requests.Load() != 3 || len(out.Calls) != 2 || !strings.Contains(out.Content, "执行失败") || !strings.Contains(out.Content, "snapshot version mismatch") {
		t.Fatal(out, err, "mutations", mutations.Load(), "reads", reads.Load(), "requests", requests.Load())
	}
}

func TestFallbackDoesNotMistakeBackupArchiveForSubmittedJob(t *testing.T) {
	jobs, _ := returnedJobs([]Invocation{{Status: "succeeded", Data: json.RawMessage(`{"data":{"id":"archive","kind":"manual","status":"failed"}}`)}})
	if len(jobs) != 0 {
		t.Fatal("archive was treated as a job", jobs)
	}
}

func TestProviderQuotaErrorPreservesMeaningWithoutExposingResponseText(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"code":"pre_consume_token_quota_failed","type":"new_api_error","message":"token quota is not enough; echoed-api-key-do-not-expose"}}`)
	}))
	defer server.Close()
	_, err := complete(context.Background(), Config{Endpoint: server.URL, Model: "test"}, "private-key", []Message{{Role: "user", Content: "check"}}, nil)
	if err == nil || err.Error() != "AI_PROVIDER_QUOTA_EXHAUSTED" || requests.Load() != 1 || strings.Contains(err.Error(), "do-not-expose") {
		t.Fatal("quota rejection was misclassified, retried, or leaked response text", err, requests.Load())
	}
}

func TestChatQuotaAfterDiscoveryReportsNoMutationExecuted(t *testing.T) {
	var requests, mutations atomic.Int32
	s, store := fixture(t, backendFunc(func(context.Context, string, Arguments, string) (json.RawMessage, error) {
		mutations.Add(1)
		return nil, ErrUnavailable
	}))
	catalog, _ := apicontract.Default()
	s.ConfigureAPI(catalog)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"id":"discover","type":"function","function":{"name":"find_api","arguments":"{\"query\":\"runAutomationTask\",\"limit\":1}"}}]}}]}`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"code":"pre_consume_token_quota_failed","message":"token quota is not enough"}}`)
	}))
	defer server.Close()
	config, _ := store.Config()
	config.Endpoint, config.Model = server.URL, "test"
	if _, err := store.SaveConfig(ConfigInput{Config: config}); err != nil {
		t.Fatal(err)
	}
	out, err := s.Chat(context.Background(), ChatInput{Messages: []Message{{Role: "user", Content: "test backup"}}}, func(ChatEvent) {})
	if err != nil || mutations.Load() != 0 || len(out.Calls) != 1 || !strings.Contains(out.Content, "额度不足") || !strings.Contains(out.Content, "本轮尚未执行管理修改") {
		t.Fatal("discovery-only failure misrepresented execution", out, err, mutations.Load())
	}
}
