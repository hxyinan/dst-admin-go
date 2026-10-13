package jobs

import (
	"context"
	"testing"
	"time"
)

func TestCompletionListenerSeesPersistedPartialResultOnce(t *testing.T) {
	service, store := newTestJobService(t)
	completed := make(chan Job, 2)
	service.AddCompletionListener(func(Job) { panic("listener failure") })
	service.AddCompletionListener(func(job Job) {
		persisted, err := store.Get(job.ID)
		if err != nil || persisted.Status != StatusFailed || persisted.Outcome != OutcomePartial {
			t.Error("listener called before persistence", persisted, err)
		}
		completed <- job
	})
	job, err := service.Submit("room.start", "room-1", "", []TargetSpec{{ID: "Master", Name: "Master"}, {ID: "Caves", Name: "Caves"}}, func(_ context.Context, report func(TargetResult)) error {
		report(TargetResult{TargetID: "Master", Status: StatusSucceeded})
		report(TargetResult{TargetID: "Caves", Status: StatusFailed, Error: &Error{Code: "START_FAILED", Message: "failed"}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-completed:
		if result.ID != job.ID || result.Error == nil || result.Error.Code != "PARTIAL_FAILURE" {
			t.Fatal(result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("completion notification missing")
	}
	select {
	case <-completed:
		t.Fatal("duplicate completion")
	case <-time.After(25 * time.Millisecond):
	}
}
