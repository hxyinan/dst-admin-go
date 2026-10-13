package steamlogin

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"dont/shared"
)

func TestExecutableDirectoryCommandAndSymlinkShareCredentials(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses Unix executable symlinks")
	}
	m, exe := fixture(t, "")
	dir := t.TempDir()
	launcher := filepath.Join(dir, "steamcmd.sh")
	if err := os.Symlink(exe, launcher); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "steamcmd"), []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if got := ResolveExecutable(dir); got != launcher {
		t.Fatal("directory selected a different launcher", got)
	}
	t.Setenv("PATH", dir)
	if got := ResolveExecutable("steamcmd.sh"); got != launcher {
		t.Fatal("command unavailable", got)
	}
	configure(t, m, exe)
	for _, path := range []string{dir, launcher, "steamcmd.sh"} {
		status, err := m.Status(path)
		if err != nil || status.Username != "steam_user" {
			t.Fatal("same executable has different credentials", status, err)
		}
	}
	if err := m.Run(context.Background(), dir, []string{"+login", "anonymous", "+quit"}, io.Discard); err != nil {
		t.Fatal("directory could not execute download", err)
	}
	if _, err := m.Manage(dir, shared.SteamLoginRequest{Action: "reset"}); err != nil {
		t.Fatal(err)
	}
	if status, err := m.Status(exe); err != nil || status.Mode != "anonymous" {
		t.Fatal("directory reset used a different profile", status, err)
	}
}
