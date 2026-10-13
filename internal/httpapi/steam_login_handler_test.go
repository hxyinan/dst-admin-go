package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"dont/internal/agents"
	"dont/internal/gameinstall"
	"dont/internal/steamlogin"
	"dont/shared"
	"github.com/gin-gonic/gin"
)

type steamLoginTargets struct{ exe string }

func (f steamLoginTargets) RuntimeTargets() ([]agents.RuntimeTarget, error) {
	return []agents.RuntimeTarget{{ID: "local", Online: true, Installations: []agents.RuntimeInstallation{{ID: "default", SteamCMDPath: f.exe}}}}, nil
}

func TestSteamLoginHTTPAcceptsInstallationDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable symlink fixture")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.Symlink(exe, filepath.Join(dir, "steamcmd.sh")); err != nil {
		t.Fatal(err)
	}
	// This availability flag controls whether the UI enables Steam login.
	options := gameinstall.Options{ServerPath: filepath.Join(t.TempDir(), "server"), SavePath: filepath.Join(t.TempDir(), "saves"), SteamCMDPath: dir}
	if !options.Inspect().SteamCMDAvailable {
		t.Fatal("directory configuration disabled the UI entry")
	}
	service := gameinstall.NewService(steamLoginTargets{dir}, nil, nil)
	service.ConfigureSteamLogin(steamlogin.New(filepath.Join(t.TempDir(), "auth")))
	router := gin.New()
	NewGameInstallationHandler(service).Register(router.Group("/api/v2"))
	body := map[string]interface{}{"targetId": "local", "installationId": "default", "action": "configure", "mode": "account", "username": "directory_user", "password": "test-only-password"}
	response := performJSON(router, "POST", "/api/v2/runtime-targets/game-installations/steam-login", body, nil, "")
	assertStatus(t, response, 200)
	if !strings.Contains(response.Body.String(), `"username":"directory_user"`) {
		t.Fatal("directory did not configure account", response.Body.String())
	}
}
func (steamLoginTargets) ExecuteRuntime(context.Context, string, shared.RuntimeOperationRequest, int) (agents.RuntimeExecutionResult, error) {
	panic("local login used a loopback Agent")
}
func TestSteamLoginHTTPReturnsOnlySanitizedSettings(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	service := gameinstall.NewService(steamLoginTargets{exe}, nil, nil)
	root := filepath.Join(t.TempDir(), "auth")
	service.ConfigureSteamLogin(steamlogin.New(root))
	router := gin.New()
	NewGameInstallationHandler(service).Register(router.Group("/api/v2"))
	body := map[string]interface{}{"targetId": "local", "installationId": "default", "action": "configure", "mode": "account", "username": "api_user", "password": "api-private-password"}
	response := performJSON(router, "POST", "/api/v2/runtime-targets/game-installations/steam-login", body, nil, "")
	assertStatus(t, response, 200)
	if strings.Contains(response.Body.String(), "api-private-password") || !strings.Contains(response.Body.String(), `"passwordSet":true`) {
		t.Fatal(response.Body.String())
	}
	response = performJSON(router, "GET", "/api/v2/runtime-targets/game-installations/steam-login?targetId=local&installationId=default", nil, nil, "")
	assertStatus(t, response, 200)
	if response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), `"password":`) {
		t.Fatal(response.Body.String())
	}
	body["username"] = "user\nquit"
	assertStatus(t, performJSON(router, "POST", "/api/v2/runtime-targets/game-installations/steam-login", body, nil, ""), 422)
	// Lost/corrupt credentials remain observable and require an explicit reset.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			if err = os.WriteFile(filepath.Join(root, entry.Name()), []byte("corrupt"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	service.ConfigureSteamLogin(steamlogin.New(root))
	response = performJSON(router, "GET", "/api/v2/runtime-targets/game-installations/steam-login?targetId=local&installationId=default", nil, nil, "")
	assertStatus(t, response, 200)
	if !strings.Contains(response.Body.String(), `"recoveryRequired":true`) || strings.Contains(response.Body.String(), "api-private-password") {
		t.Fatal(response.Body.String())
	}
	reset := map[string]interface{}{"targetId": "local", "installationId": "default", "action": "reset", "mode": "anonymous"}
	assertStatus(t, performJSON(router, "POST", "/api/v2/runtime-targets/game-installations/steam-login", reset, nil, ""), 422)
	delete(reset, "mode")
	response = performJSON(router, "POST", "/api/v2/runtime-targets/game-installations/steam-login", reset, nil, "")
	assertStatus(t, response, 200)
	if !strings.Contains(response.Body.String(), `"mode":"anonymous"`) || strings.Contains(response.Body.String(), `"recoveryRequired":true`) {
		t.Fatal(response.Body.String())
	}
}
