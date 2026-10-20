package apicontract

import (
	"fmt"
	"strings"
	"testing"
)

// Route parity alone cannot catch broken response refs or unreachable schemas.
// Validate every component and response as JSON Schema as well as every tool.
func TestAllContractSchemasAndResponseReferencesResolve(t *testing.T) {
	c, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	check := func(name string, schema map[string]any) {
		t.Helper()
		op := &Operation{ID: name, Method: "GET", catalog: c, RequestBody: Body{Content: map[string]Media{"application/json": {Schema: schema}}}}
		if _, err := op.ToolSchema(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name := range c.schemas {
		check(name, map[string]any{"$ref": "#/components/schemas/" + name})
	}
	for _, op := range c.ordered {
		if _, err := op.AIInputSchema(); err != nil {
			t.Errorf("AI input %s: %v", op.ID, err)
		}
		if len(op.Responses) == 0 {
			t.Errorf("%s has no responses", op.ID)
		}
		for _, segment := range strings.Split(op.Path, "/") {
			if !strings.HasPrefix(segment, "{") {
				continue
			}
			name := strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}")
			found := false
			for _, p := range op.Parameters {
				if p.Name == name && p.In == "path" && p.Required {
					found = true
				}
			}
			if !found {
				t.Errorf("%s missing required path parameter %s", op.ID, name)
			}
		}
		for status, response := range op.Responses {
			if response.Ref != "" {
				var ok bool
				response, ok = c.responses[strings.TrimPrefix(response.Ref, "#/components/responses/")]
				if !ok {
					t.Errorf("%s: unresolved response %s", op.ID, status)
					continue
				}
			}
			if strings.HasPrefix(status, "2") && status != "204" && len(response.Content) == 0 {
				t.Errorf("%s %s lacks a response schema", op.ID, status)
			}
			for media, value := range response.Content {
				if value.Schema != nil {
					check(fmt.Sprint(op.ID, " ", status, " ", media), value.Schema)
				}
			}
		}
	}
}

func TestDiscoveryPrioritizesExactOperationAndLoadsMutations(t *testing.T) {
	c, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"configureRoomBackupSchedule", "api_configureRoomBackupSchedule"} {
		items := c.Search(query, 1)
		if len(items) != 1 || items[0].ID != "configureRoomBackupSchedule" {
			t.Fatal("exact operation lost", query, items)
		}
	}
	found := false
	for _, op := range c.Search("configure backup", 8) {
		if op.ID == "configureRoomBackupSchedule" {
			found = true
		}
	}
	if !found {
		t.Fatal("backup mutation not discoverable")
	}
}

func BenchmarkDiscovery(b *testing.B) {
	c, _ := Default()
	b.ResetTimer()
	for b.Loop() {
		c.Search("backup configure", 8)
	}
}
func BenchmarkValidateOperation(b *testing.B) {
	c, _ := Default()
	op, _ := c.Operation("configureRoomBackupSchedule")
	op.ToolSchema()
	b.ResetTimer()
	for b.Loop() {
		_, _ = op.Validate([]byte(`{"path":{"roomId":"room-a"},"body":{"enabled":true},"requestId":"benchmark-001"}`))
	}
}
