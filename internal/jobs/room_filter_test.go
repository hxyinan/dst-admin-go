package jobs

import "testing"

func TestRoomFilterDoesNotLoseOlderTasksBehindOtherRooms(t *testing.T) {
	_, store := newTestJobService(t)
	first, _, err := store.Create("room.save", "room-a", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 105; i++ {
		if _, _, err := store.Create("room.start", "room-b", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	items, total, err := store.List(ListFilter{RoomID: "room-a", Limit: 25})
	if err != nil || total != 1 || len(items) != 1 || items[0].ID != first.ID {
		t.Fatalf("room task lost: %+v %d %v", items, total, err)
	}
}
