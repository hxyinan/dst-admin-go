package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"dont/internal/agents"
	"dont/internal/gameinstall"
	"dont/internal/operationlease"
	"dont/shared"
	"github.com/jinzhu/gorm"
	_ "github.com/mattn/go-sqlite3"
)

func TestAgentSteamLoginPersistsLocallyAndNeverInOperationHistory(t *testing.T) {
	a, i := newShardOperationAgent(t, &fakeShardRuntime{})
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	i.SteamCMDPath = exe
	a.Config.RuntimeInstallations[0] = i
	r := gameInstallationRequest(shared.RuntimeActionSteamLoginManage)
	r.GameInstallation.SteamLogin = &shared.SteamLoginRequest{Action: "configure", Mode: "account", Username: "remote_user", Password: "remote-private-password"}
	result, err := a.executeRuntimeOperation(string(r.Action), &r, 30)
	if err != nil || result.GameInstallation == nil || result.GameInstallation.SteamLogin == nil || !result.GameInstallation.SteamLogin.PasswordSet {
		t.Fatal(result, err)
	}
	if !shared.RuntimeActionMutates(r.Action) {
		t.Fatal("credentials can change without a lease")
	}
	data, err := os.ReadFile(a.Config.OperationStateFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "remote-private-password") {
		t.Fatal("plaintext password in operation history")
	}
	if installation, exists := a.runtimeInstallation(i.ID); !exists || installation.SteamLogin != a.Config.SteamLogin {
		t.Fatal("mod download missed node credentials")
	}
	r = gameInstallationRequest(shared.RuntimeActionSteamLoginObserve)
	r.GameInstallation.SteamLogin = &shared.SteamLoginRequest{Action: "status"}
	r.LeaseID = ""
	r.FencingToken = 0
	result, err = a.executeRuntimeOperation(string(r.Action), &r, 30)
	if err != nil || result.GameInstallation.SteamLogin.Username != "remote_user" {
		t.Fatal(result, err)
	}
	r.GameInstallation.SteamLogin.Action = "configure"
	if err = validateRuntimeOperationRequest(string(r.Action), r, 30, agentNow()); err == nil {
		t.Fatal("mutating command in observe payload accepted")
	}
	r.GameInstallation.SteamLogin.Action = "reset"
	if err = validateRuntimeOperationRequest(string(r.Action), r, 30, agentNow()); err == nil {
		t.Fatal("reset accepted in an observation")
	}
	r.Action = shared.RuntimeActionSteamLoginManage
	if err = validateRuntimeOperationRequest(string(r.Action), r, 30, agentNow()); err == nil {
		t.Fatal("reset accepted without a lease")
	}
}

type steamLoginAgentTargets struct {
	a    *Agent
	last shared.RuntimeOperationRequest
}

func (f *steamLoginAgentTargets) RuntimeTargets() ([]agents.RuntimeTarget, error) {
	return []agents.RuntimeTarget{{ID: "agent:login-test", Online: true, Capabilities: []string{shared.SteamLoginCapability, "runtime.game-install.v1"}, Installations: []agents.RuntimeInstallation{{ID: "default"}}}}, nil
}
func (f *steamLoginAgentTargets) ExecuteRuntime(ctx context.Context, _ string, r shared.RuntimeOperationRequest, timeout int) (agents.RuntimeExecutionResult, error) {
	f.last = r
	result, err := f.a.executeRuntimeOperationContext(ctx, string(r.Action), &r, timeout)
	return agents.RuntimeExecutionResult{Result: result}, err
}

func TestSteamLoginAndInstallationUseIndependentPersistentLeaseScopes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory adoption uses a symlink")
	}
	a, installation := newShardOperationAgent(t, &fakeShardRuntime{})
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	installation.SteamCMDPath = exe
	a.Config.RuntimeInstallations[0] = installation
	db, err := gorm.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.LogMode(false)
	db.DB().SetMaxOpenConns(1)
	leases := operationlease.NewService(db, "steam_scopes_")
	if err = leases.Migrate(); err != nil {
		t.Fatal(err)
	}
	targets := &steamLoginAgentTargets{a: a}
	s := gameinstall.NewService(targets, nil, leases)
	configure := shared.SteamLoginRequest{Action: "configure", Mode: "account", Username: "test_user", Password: "test-only-password"}
	if _, err = s.SteamLogin(context.Background(), "agent:login-test", "default", configure); err != nil {
		t.Fatal("configure before installation", err)
	}
	firstLogin := targets.last
	source := filepath.Join(filepath.Dir(installation.ServerPath), "existing-game")
	writeInstallationGame(t, source)
	probe := gameInstallationRequest(shared.RuntimeActionGameInstallationObserve)
	probe.GameInstallation.Path = source
	observed, err := a.executeRuntimeOperation(string(probe.Action), &probe, 30)
	if err != nil {
		t.Fatal(err)
	}
	var firstInstall shared.RuntimeOperationRequest
	for _, key := range []string{"install-one", "install-two"} {
		path := source
		if key == "install-two" {
			path += "-two"
			writeInstallationGame(t, path)
			probe.GameInstallation.Path = path
			observed, err = a.executeRuntimeOperation(string(probe.Action), &probe, 30)
			if err != nil {
				t.Fatal(err)
			}
		}
		lease, err := leases.Acquire(context.Background(), "game-install:agent:login-test:default", key, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		r := gameInstallationRequest(shared.RuntimeActionGameInstallationAdopt)
		r.Cluster, r.Shard = "GameInstallation", "Runtime"
		r.OperationID, r.OperationKey = key, key
		r.GameInstallation = &shared.GameInstallationRequest{Path: path, Fingerprint: observed.GameInstallation.Fingerprint}
		r.LeaseID, r.FencingToken, r.LeaseExpiresAt = lease.LeaseID, lease.FencingToken, &lease.ExpiresAt
		result, operationErr := a.executeRuntimeOperation(string(r.Action), &r, 30)
		leases.Release(lease)
		if key == "install-one" && (operationErr != nil || result.Outcome != shared.RuntimeOutcomeConfirmed) {
			t.Fatal("installation after configuring Steam", result, operationErr)
		}
		if key == "install-two" && !errors.Is(operationErr, gameinstall.ErrOccupied) {
			t.Fatal("valid installation lease rejected by Steam login scope", operationErr)
		}
		if firstInstall.OperationKey == "" {
			firstInstall = r
		}
	}
	if _, err = s.SteamLogin(context.Background(), "agent:login-test", "default", configure); err != nil {
		t.Fatal("configure after installation", err)
	}
	// Both independent domains must still reject genuinely stale mutations after
	// Agent restart, while preserving the account and the adopted installation.
	restarted, err := NewAgent(&Config{ServerURL: a.Config.ServerURL, AgentID: "test", KeyFile: a.Config.KeyFile, OperationStateFile: a.Config.OperationStateFile, RuntimeInstallations: []RuntimeInstallation{installation}})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []shared.RuntimeOperationRequest{firstLogin, firstInstall} {
		request.OperationKey += "-stale"
		request.OperationID += "-stale"
		if _, err = restarted.executeRuntimeOperation(string(request.Action), &request, 30); err == nil || !strings.Contains(err.Error(), "fencing token 已过期") {
			t.Fatal("stale mutation accepted", request.Action, err)
		}
	}
	targets.a = restarted
	status, err := s.SteamLogin(context.Background(), "agent:login-test", "default", shared.SteamLoginRequest{Action: "reset"})
	if err != nil || status.Mode != "anonymous" || status.PasswordSet {
		t.Fatal("Agent reset failed", status, err)
	}
	if b, err := os.ReadFile(filepath.Join(installation.SavePath, "Cluster_1", "cluster.ini")); err != nil || string(b) != "[NETWORK]\n" {
		t.Fatal("saves changed", err)
	}
}
