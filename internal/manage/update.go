package manage

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
)

// gitEnv returns the environment for git commands, routing them through the
// tunnel's SOCKS5 proxy when one is available — so a server with GitHub blocked
// (e.g. Iran) reaches GitHub through its peer (kharej).
func gitEnv() []string {
	env := os.Environ()
	if proxy := socksProxyURL(); proxy != "" {
		env = append(env, "ALL_PROXY="+proxy, "HTTPS_PROXY="+proxy, "HTTP_PROXY="+proxy)
	}
	return env
}

// socksProxyURL builds a socks5h URL for the built-in SOCKS relay if any server
// tunnel exposes it, or "" if none does.
func socksProxyURL() string {
	suffix := fmt.Sprintf("=127.0.0.1:%d", app.SocksInternalPort)
	matches, _ := filepath.Glob(app.ConfigDir + "/*.toml")
	for _, path := range matches {
		var cfg config.Config
		if _, err := toml.DecodeFile(path, &cfg); err != nil || cfg.Server.BindAddr == "" {
			continue
		}
		for _, p := range cfg.Server.Ports {
			if strings.HasSuffix(strings.TrimSpace(p), suffix) {
				port := strings.TrimSuffix(strings.TrimSpace(p), suffix)
				return fmt.Sprintf("socks5h://backpack:%s@127.0.0.1:%s", cfg.Server.Token, port)
			}
		}
	}
	return ""
}

// InstallPath returns the cloned repo directory recorded at install time, or ""
// (used by the updater and the uninstaller).
func InstallPath() string {
	b, err := os.ReadFile(app.InstallPathFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func isGitRepo(path string) bool {
	if path == "" {
		return false
	}
	out, err := exec.Command("git", "-C", path, "rev-parse", "--is-inside-work-tree").Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func findGo() string {
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	if _, err := os.Stat("/usr/local/go/bin/go"); err == nil {
		return "/usr/local/go/bin/go"
	}
	return ""
}

// CheckUpdate fetches the cloned repo's remote and reports whether it is behind.
func CheckUpdate() (bool, string, error) {
	path := InstallPath()
	if !isGitRepo(path) {
		return false, "", fmt.Errorf("not installed from a git clone — reinstall via 'git clone' to enable updates")
	}
	fetch := exec.Command("git", "-C", path, "fetch", "--quiet")
	fetch.Env = gitEnv()
	if err := fetch.Run(); err != nil {
		return false, "", fmt.Errorf("git fetch failed: %w", err)
	}
	local := gitRev(path, "HEAD")
	remote := gitRev(path, "@{u}")
	if remote == "" {
		return false, "", fmt.Errorf("no upstream branch configured")
	}
	if local == remote {
		return false, "Already up to date.", nil
	}
	count := strings.TrimSpace(gitOut(path, "rev-list", "--count", "HEAD..@{u}"))
	return true, fmt.Sprintf("%s new commit(s) available.", count), nil
}

// ApplyUpdate pulls the latest code, rebuilds the binary, and restarts the web
// panel and all tunnels.
func ApplyUpdate(logf func(string)) error {
	path := InstallPath()
	if !isGitRepo(path) {
		return fmt.Errorf("not installed from a git clone — reinstall via 'git clone'")
	}

	logf("Pulling latest changes...")
	pull := exec.Command("git", "-C", path, "pull", "--ff-only")
	pull.Env = gitEnv()
	if out, err := pull.CombinedOutput(); err != nil {
		return fmt.Errorf("git pull failed: %s", strings.TrimSpace(string(out)))
	}

	goBin := findGo()
	if goBin == "" {
		return fmt.Errorf("Go toolchain not found — run 'sudo bash install.sh' in %s", path)
	}

	logf("Rebuilding...")
	build := exec.Command(goBin, "build", "-trimpath", "-ldflags", "-s -w", "-o", app.BinPath, ".")
	build.Dir = path
	build.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOTOOLCHAIN=local",
		"GOSUMDB=off",
		"GOPROXY=https://mirror-go.runflare.com,https://goproxy.cn,https://goproxy.io,direct",
	)
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("build failed: %s", strings.TrimSpace(string(out)))
	}

	logf("Restarting services...")
	RestartService(app.WebUIService)
	ok, failed := RestartAll()
	logf(fmt.Sprintf("Restarted %d tunnels (%d failed).", ok, failed))
	logf("Update complete.")
	return nil
}

func gitRev(path, ref string) string { return strings.TrimSpace(gitOut(path, "rev-parse", ref)) }

func gitOut(path string, args ...string) string {
	out, _ := exec.Command("git", append([]string{"-C", path}, args...)...).Output()
	return string(out)
}
