package steamlogin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"dont/shared"
)

// The test executable acts as SteamCMD in subprocesses. This exercises actual
// stdin/stdout fragmentation, process cancellation and argv without a network
// dependency or real Steam credentials.
func TestMain(m *testing.M) {
	if os.Getenv("DST_STEAM_LOGIN_TEST_PROCESS") == "1" {
		mockSteam()
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func mockSteam() {
	if file := os.Getenv("DST_STEAM_LOGIN_TEST_ARGV"); file != "" {
		data, _ := json.Marshal(os.Args)
		_ = os.WriteFile(file, data, 0o600)
	}
	if len(os.Args) > 1 {
		fmt.Println("anonymous command completed")
		return
	}
	input := bufio.NewScanner(os.Stdin)
	for input.Scan() {
		line := input.Text()
		if file := os.Getenv("DST_STEAM_LOGIN_TEST_COMMANDS"); file != "" {
			f, _ := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if f != nil {
				_, _ = fmt.Fprintln(f, line)
				_ = f.Close()
			}
		}
		if strings.HasPrefix(line, "login ") {
			if os.Getenv("DST_STEAM_LOGIN_TEST_MODE") == "keywords" {
				fmt.Println("Steam>" + line)
				fmt.Println("Logging in user 'InvalidPassword' to Steam Public...OK")
			}
			switch os.Getenv("DST_STEAM_LOGIN_TEST_MODE") {
			case "invalid":
				fmt.Println("ERROR! Failed to login: InvalidPassword")
				return
			case "hang":
				for input.Scan() {
				}
				return
			case "approval":
				fmt.Print("Please confirm the login in your Steam mobile app:")
				deadline := time.Now().Add(5 * time.Second)
				for {
					if _, err := os.Stat(os.Getenv("DST_STEAM_LOGIN_TEST_VERIFIED")); err == nil {
						break
					}
					if time.Now().After(deadline) {
						return
					}
					time.Sleep(5 * time.Millisecond)
				}
			case "email", "mobile":
				marker := os.Getenv("DST_STEAM_LOGIN_TEST_VERIFIED")
				if _, err := os.Stat(marker); err != nil {
					if os.Getenv("DST_STEAM_LOGIN_TEST_MODE") == "email" {
						fmt.Print("Please enter the Steam Guard code from the email sent to you:")
					} else {
						fmt.Print("Please enter the current auth code from your Steam Guard Mobile Authenticator app:")
					}
					if !input.Scan() || input.Text() != "ABCDE" {
						fmt.Println("ERROR! Failed to login")
						return
					}
					if marker != "" {
						_ = os.WriteFile(marker, []byte("verified"), 0o600)
					}
				}
			}
			fmt.Print("fragmented-secret-")
			fmt.Print("password\n")
			fmt.Println("Waiting for user info...OK")
		} else if strings.HasPrefix(line, "app_update ") {
			if os.Getenv("DST_STEAM_LOGIN_TEST_MODE") == "denied" {
				fmt.Println("ERROR! Failed to install app '343050' (No subscription)")
				// A later successful command in a batch must not hide this error.
				fmt.Println(strings.Repeat("later batch progress ", 5000))
			} else {
				fmt.Println("Success! App '343050' fully installed.")
			}
		} else if strings.HasPrefix(line, "workshop_download_item ") {
			if os.Getenv("DST_STEAM_LOGIN_TEST_MODE") == "disk" && strings.Contains(line, `"111"`) {
				fmt.Print("Update canceled: Staging folder not ")
				fmt.Println("writable (Disk write failure)")
			} else {
				fmt.Println("Success. Downloaded item 222")
			}
		} else if line == "quit" {
			return
		}
	}
}
func fixture(t *testing.T, mode string) (*Manager, string) {
	t.Helper()
	t.Setenv("DST_STEAM_LOGIN_TEST_PROCESS", "1")
	t.Setenv("DST_STEAM_LOGIN_TEST_MODE", mode)
	t.Setenv("DST_STEAM_LOGIN_TEST_VERIFIED", filepath.Join(t.TempDir(), "verified"))
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	m := New(filepath.Join(t.TempDir(), "steam-auth"))
	t.Cleanup(m.Close)
	return m, executable
}
func configure(t *testing.T, m *Manager, exe string) {
	t.Helper()
	_, err := m.Manage(exe, shared.SteamLoginRequest{Action: "configure", Mode: "account", Username: "steam_user", Password: "fragmented-secret-password"})
	if err != nil {
		t.Fatal(err)
	}
}
func waitState(t *testing.T, m *Manager, exe string, want string) shared.SteamLoginStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := m.Status(exe)
		if err != nil {
			t.Fatal(err)
		}
		if status.State == want {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	status, _ := m.Status(exe)
	t.Fatalf("wanted %s, got %+v", want, status)
	return status
}
func TestEncryptedCredentialsPersistAndAnonymousClearsAccount(t *testing.T) {
	m, exe := fixture(t, "")
	status, err := m.Status(exe)
	if err != nil || status.Mode != "anonymous" {
		t.Fatal(status, err)
	}
	if _, err = os.Stat(m.root); !os.IsNotExist(err) {
		t.Fatal("anonymous observation wrote files")
	}
	configure(t, m, exe)
	entries, err := os.ReadDir(m.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(m.root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("fragmented-secret-password")) {
			t.Fatal("plaintext password persisted")
		}
		info, _ := entry.Info()
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatal("credentials permission", info.Mode())
		}
	}
	reopened := New(m.root)
	status, err = reopened.Status(exe)
	if err != nil || !status.PasswordSet || status.Username != "steam_user" {
		t.Fatal(status, err)
	}
	_, err = reopened.Manage(exe, shared.SteamLoginRequest{Action: "configure", Mode: "account", Username: "steam_user"})
	if err != nil {
		t.Fatal("same-account password retention", err)
	}
	if _, err = reopened.Manage(exe, shared.SteamLoginRequest{Action: "configure", Mode: "account", Username: "other_user"}); err == nil {
		t.Fatal("password reused for another user")
	}
	_, err = reopened.Manage(exe, shared.SteamLoginRequest{Action: "configure", Mode: "anonymous"})
	if err != nil {
		t.Fatal(err)
	}
	status, err = New(m.root).Status(exe)
	if err != nil || status.Mode != "anonymous" || status.PasswordSet || status.Username != "" {
		t.Fatal(status, err)
	}
}
func TestAccountDownloadUsesStdinAndRedactsFragmentedSecrets(t *testing.T) {
	m, exe := fixture(t, "")
	configure(t, m, exe)
	argv := filepath.Join(t.TempDir(), "argv")
	commands := filepath.Join(t.TempDir(), "commands")
	t.Setenv("DST_STEAM_LOGIN_TEST_ARGV", argv)
	t.Setenv("DST_STEAM_LOGIN_TEST_COMMANDS", commands)
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := m.Run(ctx, exe, []string{"+force_install_dir", "/game path", "+login", "anonymous", "+app_update", "343050", "validate", "+quit"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(argv)
	var args []string
	if json.Unmarshal(data, &args) != nil || len(args) != 1 {
		t.Fatalf("credentials in argv: %s", data)
	}
	if strings.Contains(output.String(), "fragmented-secret-password") || !strings.Contains(output.String(), "[redacted]") {
		t.Fatal("secret redaction failed", output.String())
	}
	data, _ = os.ReadFile(commands)
	lines := strings.Split(string(data), "\n")
	if !strings.HasPrefix(lines[0], "force_install_dir ") || !strings.HasPrefix(lines[1], "login ") || !strings.HasPrefix(lines[2], "app_update ") {
		t.Fatal("wrong command order", lines)
	}
}
func TestGuardVerificationAndSubsequentUnattendedDownload(t *testing.T) {
	for _, kind := range []string{"email", "mobile"} {
		t.Run(kind, func(t *testing.T) {
			m, exe := fixture(t, kind)
			configure(t, m, exe)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := m.Run(ctx, exe, []string{"+login", "anonymous", "+app_update", "343050", "+quit"}, io.Discard)
			if errorCode(err) != "GUARD_REQUIRED" {
				t.Fatal("unattended task waited for code", err)
			}
			if _, err = m.Manage(exe, shared.SteamLoginRequest{Action: "login"}); err != nil {
				t.Fatal(err)
			}
			guard := waitState(t, m, exe, "guard_"+kind)
			if _, err = m.Manage(exe, shared.SteamLoginRequest{Action: "configure", Mode: "anonymous"}); err != ErrBusy {
				t.Fatal("changed credentials during login", err)
			}
			if _, err = m.Manage(exe, shared.SteamLoginRequest{Action: "code", SessionID: "other-session", Code: "ABCDE"}); err == nil {
				t.Fatal("wrong session accepted")
			}
			if _, err = m.Manage(exe, shared.SteamLoginRequest{Action: "code", SessionID: guard.SessionID, Code: "abcde"}); err != nil {
				t.Fatal(err)
			}
			waitState(t, m, exe, "ready")
			if err = m.Run(ctx, exe, []string{"+login", "anonymous", "+app_update", "343050", "+quit"}, io.Discard); err != nil {
				t.Fatal("download after verification", err)
			}
		})
	}
}
func TestMobileApprovalLogin(t *testing.T) {
	m, exe := fixture(t, "approval")
	configure(t, m, exe)
	if _, err := m.Manage(exe, shared.SteamLoginRequest{Action: "login"}); err != nil {
		t.Fatal(err)
	}
	guard := waitState(t, m, exe, "guard_approval")
	if _, err := m.Manage(exe, shared.SteamLoginRequest{Action: "code", SessionID: guard.SessionID, Code: "ABCDE"}); err == nil {
		t.Fatal("mobile approval incorrectly accepted an OTP")
	}
	if err := os.WriteFile(os.Getenv("DST_STEAM_LOGIN_TEST_VERIFIED"), []byte("approved"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, exe, "ready")
}
func TestOverlongFragmentedOutputNeverRevealsPartOfSecret(t *testing.T) {
	for _, whole := range []bool{false, true} {
		var output bytes.Buffer
		s := &loginSession{output: &output, secrets: []string{"private-password"}}
		if whole {
			_, _ = s.Write([]byte(strings.Repeat("x", 65536) + "private-password\n"))
		} else {
			_, _ = s.Write([]byte(strings.Repeat("x", 65536) + "private-"))
			if len(s.pending) > 65536 {
				t.Fatal("output buffer is unbounded")
			}
			_, _ = s.Write([]byte("password\n"))
		}
		_, _ = s.Write([]byte("normal output\n"))
		if strings.Contains(output.String(), "private-") || strings.Contains(output.String(), "password") || !strings.Contains(output.String(), "normal output") {
			t.Fatal("overlong output leaked a fragment or hid later lines", output.String())
		}
	}
}
func TestAnonymousUseBlocksCredentialChangesUntilAllDownloadsFinish(t *testing.T) {
	m, exe := fixture(t, "")
	_, release1, err := m.AnonymousUsage(exe)
	if err != nil {
		t.Fatal(err)
	}
	_, release2, err := m.AnonymousUsage(exe)
	if err != nil {
		t.Fatal(err)
	}
	release1()
	release1()
	if _, err = m.Manage(exe, shared.SteamLoginRequest{Action: "configure", Mode: "account", Username: "steam_user", Password: "password"}); err != ErrBusy {
		t.Fatal("credentials changed while another download was running", err)
	}
	release2()
	configure(t, m, exe)
}
func TestLoginCancellationAndInvalidPassword(t *testing.T) {
	m, exe := fixture(t, "hang")
	configure(t, m, exe)
	status, err := m.Manage(exe, shared.SteamLoginRequest{Action: "login"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Manage(exe, shared.SteamLoginRequest{Action: "cancel", SessionID: status.SessionID}); err != nil {
		t.Fatal(err)
	}
	status = waitState(t, m, exe, "failed")
	if status.ErrorCode != "LOGIN_CANCELLED" {
		t.Fatal(status)
	}
	t.Setenv("DST_STEAM_LOGIN_TEST_MODE", "invalid")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err = m.Run(ctx, exe, []string{"+login", "anonymous", "+quit"}, io.Discard); errorCode(err) != "INVALID_PASSWORD" {
		t.Fatal(err)
	}
}
func TestDownloadFailureWithZeroExitStatusIsNotSuccess(t *testing.T) {
	m, exe := fixture(t, "denied")
	configure(t, m, exe)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.Run(ctx, exe, []string{"+login", "anonymous", "+app_update", "343050", "+quit"}, io.Discard); errorCode(err) != "ACCESS_DENIED" {
		t.Fatal("false success", err)
	}
}
func TestValidationRejectsCommandInjection(t *testing.T) {
	for _, input := range []shared.SteamLoginRequest{
		{Action: "configure", Mode: "account", Username: "user\nquit", Password: "password"},
		{Action: "configure", Mode: "account", Username: "user", Password: "password\rquit"},
		{Action: "code", SessionID: "session", Code: "A\nB"},
		{Action: "status", Password: "password"},
		{Action: "reset", Mode: "anonymous"},
		{Action: "reset", Username: "user"},
		{Action: "reset", Password: "password"},
		{Action: "reset", SessionID: "session"},
		{Action: "reset", Code: "ABCDE"},
	} {
		if Validate(input) == nil {
			t.Fatal("invalid input accepted")
		}
	}
	if _, err := downloadCommands([]string{"+login", "anonymous", "+quit", "+runscript", "/tmp/script"}); err == nil {
		t.Fatal("arbitrary command accepted")
	}
}
