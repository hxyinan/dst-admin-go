package apicontract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestShippedContractGeneratesEveryOperation(t *testing.T) {
	c, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Operations()) < 290 {
		t.Fatal("management operations disappeared", len(c.Operations()))
	}
	for _, op := range c.Operations() {
		t.Run(op.ID, func(t *testing.T) {
			if _, err := op.ToolSchema(); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, id := range []string{"chatWithAI", "login", "setupAdmin", "logout", "getAuthSession"} {
		op, ok := c.Operation(id)
		if !ok || op.Callable() {
			t.Fatalf("transport entry exposed as a management tool: %s", id)
		}
	}
}

func TestSchemaRejectsUnknownFieldsAndEscapingPaths(t *testing.T) {
	c, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	op, ok := c.Operation("updateRoomBackupPolicy")
	if !ok {
		t.Fatal("backup operation missing")
	}
	valid := `{"path":{"roomId":"room-a"},"body":{"enabled":true,"intervalMinutes":60,"maxSnapshots":7},"requestId":"backup-0001"}`
	in, err := op.Validate(json.RawMessage(valid))
	if err != nil {
		t.Fatal(err)
	}
	if uri, err := op.URL(in); err != nil || uri != "/api/v2/rooms/room-a/backup-policy" {
		t.Fatal(uri, err)
	}
	for _, raw := range []string{strings.Replace(valid, `"enabled":true`, `"enabled":"yes"`, 1), strings.Replace(valid, `"maxSnapshots":7`, `"maxSnapshots":0`, 1), strings.Replace(valid, `"body":{`, `"url":"http://outside.invalid","body":{`, 1)} {
		if _, err := op.Validate(json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid input accepted: %s", raw)
		}
	}
	in.Path["roomId"] = "../other"
	if _, err := op.URL(in); err == nil {
		t.Fatal("path traversal accepted")
	}
}
