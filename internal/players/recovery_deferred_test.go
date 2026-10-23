package players

import (
	"context"
	"errors"
	"testing"

	"dont/internal/dstruntime"
)

func TestExplicitRefreshMustReportNotCollected(t *testing.T) {
	service, _, _, _, probe := newPlayerTestService(t)
	probe.err = errors.Join(dstruntime.ErrSnapshotUnavailable, ErrTelemetryRecoveryDeferred)
	result, err := service.RefreshWorld(context.Background(), "room", "master")
	if err == nil {
		t.Fatalf("manual refresh returns success without collection: status=%q message=%q", result.Status, result.Message)
	}
}
