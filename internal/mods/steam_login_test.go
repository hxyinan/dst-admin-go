package mods

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"dont/internal/steamlogin"
	"dont/shared"
)

func TestSteamLoginDownloadReportsStorageFailureInBothModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell SteamCMD fixture")
	}
	for _, mode := range []string{"anonymous", "account"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			exe := filepath.Join(dir, "steamcmd.sh")
			script := `#!/bin/sh
if [ "$#" -gt 0 ]; then
  printf 'Update canceled: Staging folder not writable (Disk write failure)\n'
  exit 0
fi
while IFS= read -r command; do
  case "$command" in
    login*) printf 'Waiting for user info...OK\n' ;;
    workshop_download_item*) printf 'Update canceled: Staging folder not writable (Disk write failure)\n' ;;
    quit) exit 0 ;;
  esac
done
`
			if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			m := steamlogin.New(filepath.Join(t.TempDir(), "auth"))
			t.Cleanup(m.Close)
			if mode == "account" {
				if _, err := m.Manage(dir, shared.SteamLoginRequest{Action: "configure", Mode: mode, Username: "test_user", Password: "test-password"}); err != nil {
					t.Fatal(err)
				}
			}
			runner := NewSteamCMDRunner(dir, t.TempDir(), "322330")
			runner.SteamLogin = m
			t.Cleanup(func() { _ = runner.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := runner.Download(ctx, []string{"111"}, false, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "Disk write failure") {
				t.Fatal("storage failure was treated as success", err)
			}
		})
	}
}
