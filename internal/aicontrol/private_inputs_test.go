package aicontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dont/internal/apicontract"
)

func TestPrivateInputResolvedOnlyForExecutionAndRedactedFromHistory(t *testing.T) {
	const secret = "A9Z7Q"
	var got string
	s, store := fixture(t, apiBackendFunc(func(_ context.Context, _ Principal, _ *apicontract.Operation, input apicontract.Input) (json.RawMessage, error) {
		got = input.Body.(map[string]any)["code"].(string)
		return json.Marshal(map[string]any{"echo": got, "passwordSet": true})
	}))
	catalog, _ := apicontract.Default()
	s.ConfigureAPI(catalog)
	p := Principal{ID: "page"}
	input, err := store.SavePrivateInput(p, "Steam code", secret)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(fmt.Sprintf(`{"body":{"targetId":"local","installationId":"default","action":"code","sessionId":"session","code":%q},"requestId":"private-code-001"}`, privateInputPrefix+input.ID))
	out, err := s.Invoke(context.Background(), p, "api_manageNodeSteamLogin", raw)
	if err != nil || out.Status != "succeeded" || got != secret {
		t.Fatal(out, got, err)
	}
	call, err := store.GetCall(out.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(call.Arguments+call.Result, secret) || !strings.Contains(call.Result, `"passwordSet":true`) {
		t.Fatal("private value leaked or presence flag lost", call.Arguments, call.Result)
	}
	if _, _, err = store.resolvePrivateInputs(Principal{ID: "other"}, raw); err == nil {
		t.Fatal("cross-caller reference accepted")
	}
	plain := []byte(`{"body":{"targetId":"local","installationId":"default","action":"code","sessionId":"session","code":"Q7Z9A"},"requestId":"private-code-002"}`)
	out, err = s.Invoke(context.Background(), p, "api_manageNodeSteamLogin", plain)
	if err != nil {
		t.Fatal(err)
	}
	call, _ = store.GetCall(out.CallID)
	if strings.Contains(call.Arguments, "Q7Z9A") {
		t.Fatal("writeOnly Steam code persisted", call.Arguments)
	}
}

func TestPrivateInputValueIsNeverSentToModel(t *testing.T) {
	s, store := fixture(t, nil)
	input, err := store.SavePrivateInput(Principal{ID: "page"}, "Steam password", "very-private-steam-value")
	if err != nil {
		t.Fatal(err)
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "very-private-steam-value") || !strings.Contains(string(body), privateInputPrefix+input.ID) {
			t.Error("wrong model private input context")
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"已收到 Steam 密码引用。"}}]}`)
	}))
	defer model.Close()
	config, _ := store.Config()
	config.Endpoint = model.URL
	config.Model = "test"
	store.SaveConfig(ConfigInput{Config: config})
	_, err = s.Chat(context.Background(), ChatInput{Messages: []Message{{Role: "user", Content: "继续配置", PrivateInputs: []string{input.ID}}}}, func(ChatEvent) {})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMCPAcceptsPrivateReferenceForStrictSteamCodeField(t *testing.T) {
	var code string
	s, store := fixture(t, apiBackendFunc(func(_ context.Context, _ Principal, _ *apicontract.Operation, input apicontract.Input) (json.RawMessage, error) {
		code = input.Body.(map[string]any)["code"].(string)
		return json.RawMessage(`{"state":"ready"}`), nil
	}))
	catalog, _ := apicontract.Default()
	s.ConfigureAPI(catalog)
	token, secret, err := store.CreateToken(TokenInput{Name: "private MCP", Days: 1})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.SavePrivateInput(Principal{ID: token.ID}, "Steam code", "Q1W2E")
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"api_manageNodeSteamLogin","arguments":{"body":{"targetId":"local","installationId":"default","action":"code","sessionId":"session","code":%q},"requestId":"mcp-private-001"}}}`, privateInputPrefix+input.ID)
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	out := httptest.NewRecorder()
	s.MCP(out, req)
	if out.Code != 200 || code != "Q1W2E" || !strings.Contains(out.Body.String(), "succeeded") || strings.Contains(out.Body.String(), "Q1W2E") {
		t.Fatal(out.Code, code, out.Body)
	}
}
