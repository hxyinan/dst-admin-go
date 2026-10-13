package softwareupdate

import (
	"context"
	"testing"
)

func TestReleaseObserverUsesExistingCheckAndRunsOutsideSnapshotLock(t *testing.T) {
	s, client := testService(t, []byte("fixture"))
	var versions []string
	s.config.OnAvailable = func(version string) { _, _ = s.Snapshot(); versions = append(versions, version) }
	if _, err := s.Check(context.Background(), true, "direct"); err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0] != "v1.1.0" {
		t.Fatal("new release event missing", versions)
	}
	if _, err := s.Check(context.Background(), false, "direct"); err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || client.calls.Load() != 1 {
		t.Fatal("notification forced an extra check", versions, client.calls.Load())
	}
}
