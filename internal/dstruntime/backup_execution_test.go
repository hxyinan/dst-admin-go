package dstruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestProtectionBackupRetainsEditedExecutionScript(t *testing.T) {
	manager, catalog, root := newRuntimeTestManager(t)
	world := catalog.worlds[0]
	first, err := manager.InstallWorld(context.Background(), catalog.room.ID, world.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "Cluster_1", "Master", executionDirectory, "bootstrap.lua")
	edited := []byte("-- operator modification\nreturn true\n")
	if err := os.WriteFile(path, edited, 0640); err != nil {
		t.Fatal(err)
	}
	rolled, err := manager.RollbackWorld(context.Background(), catalog.room.ID, world.ID, filepath.Base(first.BackupPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RollbackWorld(context.Background(), catalog.room.ID, world.ID, filepath.Base(rolled.BackupPath)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(edited) {
		t.Fatalf("protection backup lost edited execution file; error=%v bytes=%d expected=%d", err, len(got), len(edited))
	}
}
