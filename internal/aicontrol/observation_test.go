package aicontrol

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestStartupPlanIsOptionalAndOnlyAnArgumentForRoomStatus(t *testing.T) {
	var plans []bool
	s, _ := fixture(t, backendFunc(func(_ context.Context, _ string, a Arguments, _ string) (json.RawMessage, error) {
		plans = append(plans, a.IncludeStartupPlan)
		return json.RawMessage(`{"shards":[]}`), nil
	}))
	for _, raw := range []string{`{"roomId":"room-a"}`, `{"roomId":"room-a","includeStartupPlan":true}`} {
		if _, err := s.Invoke(context.Background(), principal(), "room_status", json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if len(plans) != 2 || plans[0] || !plans[1] {
		t.Fatal("startup plan preference was lost", plans)
	}
	if _, err := s.Invoke(context.Background(), principal(), "world_logs", json.RawMessage(`{"roomId":"room-a","worldId":"master","includeStartupPlan":true}`)); !errors.Is(err, ErrInvalid) {
		t.Fatal("startup preference changed an unrelated tool", err)
	}
}
