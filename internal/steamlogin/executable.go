package steamlogin

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ResolveExecutable accepts an executable, an installation directory, or a
// command in PATH. All SteamCMD consumers use the same directory preference.
func ResolveExecutable(configured string) string {
	configured = strings.TrimSpace(configured)
	candidates := []string{}
	if configured != "" {
		candidates = append(candidates, configured)
		for _, name := range []string{"steamcmd.sh", "steamcmd", "steamcmd.exe"} {
			candidates = append(candidates, filepath.Join(configured, name))
		}
	}
	candidates = append(candidates, "steamcmd", "steamcmd.sh", "steamcmd.exe")
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		path, err = filepath.Abs(path)
		if err != nil {
			continue
		}
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() && (runtime.GOOS == "windows" || info.Mode().Perm()&0111 != 0) {
			return path
		}
	}
	return ""
}
