package aicontrol

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestDomainQueriesPreserveRoomScopeAndBoundPagination(t *testing.T) {
	calls := 0
	service, _ := fixture(t, backendFunc(func(context.Context, string, Arguments, string) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"items":[]}`), nil
	}))
	for _, name := range []string{"room_performance", "room_players", "room_mods", "room_backups", "room_automation"} {
		if _, err := service.Invoke(context.Background(), principal(), name, json.RawMessage(`{"roomId":"other-room"}`)); !errors.Is(err, ErrDenied) {
			t.Fatal(name, err)
		}
		if _, err := service.Invoke(context.Background(), principal(), name, json.RawMessage(`{}`)); !errors.Is(err, ErrInvalid) {
			t.Fatal("query without a room", name, err)
		}
	}
	if calls != 0 {
		t.Fatal("invalid scopes reached platform queries", calls)
	}
	for _, name := range []string{"room_players", "room_mods", "room_automation"} {
		for _, raw := range []string{`{"roomId":"room-a","offset":-1}`, `{"roomId":"room-a","limit":201}`, `{"roomId":"room-a","offset":100001}`} {
			if _, err := service.Invoke(context.Background(), principal(), name, json.RawMessage(raw)); !errors.Is(err, ErrInvalid) {
				t.Fatal(name, raw, err)
			}
		}
		if _, err := service.Invoke(context.Background(), principal(), name, json.RawMessage(`{"roomId":"room-a","offset":10,"limit":10}`)); err != nil {
			t.Fatal(name, err)
		}
	}
	if calls != 3 {
		t.Fatal("valid pages were not queried", calls)
	}
}
