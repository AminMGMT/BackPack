package manage

// Release-based updater. Backpack updates itself from GitHub release assets
// (backpack_linux_amd64.tar.gz / backpack_linux_arm64.tar.gz). Every network
// step is tried in order:
//
//  1. direct GitHub
//  2. the tunnel SOCKS relay (the peer/kharej side can reach GitHub)
//  3. public GitHub download mirrors that work from Iran

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/socks"
)

// ghMirrors are GitHub proxies (prefix form) tried after the direct and
// tunnel-relay attempts, so updates keep working where GitHub is blocked.
var ghMirrors = []string{
	"https://gh-proxy.com/",
	"https://ghfast.top/",
	"https://ghproxy.net/",
}

func repoURL() string {
	return fmt.Sprintf("https://github.com/%s/%s", app.RepoOwner, app.RepoName)
}

// InstallPath returns the install directory recorded at install time (used by
// the uninstaller), falling back to the standard location.
func InstallPath() string {
	b, err := os.ReadFile(app.InstallPathFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// relayHTTPClient returns an HTTP client routed through the tunnel SOCKS relay
// when one is configured (the port a server tunnel maps to the peer's built-in
// SOCKS5 proxy), or nil when none exists.
func relayHTTPClient(timeout time.Duration) *http.Client {
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
				return socks.HTTPClient("127.0.0.1:"+port, "backpack", cfg.Server.Token, timeout)
			}
		}
	}
	return nil
}

// source is one way to reach GitHub: a client plus a URL prefix.
type source struct {
	name   string
	client *http.Client
	prefix string // "" for direct, mirror base otherwise
}

// sources returns the ordered download paths: direct → tunnel relay → mirrors.
func sources(timeout time.Duration) []source {
	out := []source{{name: "direct", client: &http.Client{Timeout: timeout}}}
	if relay := relayHTTPClient(timeout); relay != nil {
		out = append(out, source{name: "tunnel relay", client: relay})
	}
	for _, m := range ghMirrors {
		out = append(out, source{name: m, client: &http.Client{Timeout: timeout}, prefix: m})
	}
	return out
}

// tagNameRe pulls "tag_name":"v1.3.0" out of the GitHub API JSON.
var tagNameRe = regexp.MustCompile(`"tag_name"\s*:\s*"([^"]+)"`)

// versionValidRe sanity-checks a version string read from the raw VERSION file.
var versionValidRe = regexp.MustCompile(`^v?[0-9]+(\.[0-9]+){0,3}$`)

// latestTag discovers the newest published version. It tries two methods across
// every source (direct → tunnel relay → mirrors) so it works from Iran too:
//
//  1. the GitHub API releases/latest endpoint (JSON tag_name) — the accurate
//     "latest release", used when the server or its tunnel peer can reach
//     api.github.com directly (own IP, not rate limited).
//  2. the raw VERSION file on main — the reliable path through mirrors, because
//     gh-proxy.com proxies raw.githubusercontent.com fine, whereas it 403s the
//     HTML releases page and shares an IP that hits the API rate limit. The
//     VERSION file is bumped together with each release.
func latestTag() (string, error) {
	var lastErr error = fmt.Errorf("no source reachable")
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", app.RepoOwner, app.RepoName)
	rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/VERSION", app.RepoOwner, app.RepoName)

	// 1) GitHub API JSON — accurate, works direct and via the tunnel relay.
	for _, s := range sources(20 * time.Second) {
		resp, err := s.client.Get(s.prefix + apiURL)
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			if m := tagNameRe.FindSubmatch(body); m != nil {
				return strings.TrimSpace(string(m[1])), nil
			}
		}
		lastErr = fmt.Errorf("api via %s: status %d", s.name, resp.StatusCode)
	}

	// 2) raw VERSION file — works through mirrors that rate-limit/deny the API.
	for _, s := range sources(20 * time.Second) {
		resp, err := s.client.Get(s.prefix + rawURL)
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			if v := strings.TrimSpace(string(body)); versionValidRe.MatchString(v) {
				return v, nil
			}
		}
		lastErr = fmt.Errorf("VERSION via %s: status %d", s.name, resp.StatusCode)
	}
	return "", fmt.Errorf("could not reach GitHub (direct, relay or mirrors): %v", lastErr)
}

// normVersion strips a leading "v" so v1.3.0 and 1.3.0 compare equal.
func normVersion(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }

// parseVer turns "v1.3.0" into [1 3 0]; missing or non-numeric parts become 0.
func parseVer(v string) [3]int {
	var out [3]int
	for i, part := range strings.SplitN(normVersion(v), ".", 3) {
		j := 0
		for j < len(part) && part[j] >= '0' && part[j] <= '9' {
			j++
		}
		out[i], _ = strconv.Atoi(part[:j])
	}
	return out
}

// newerVersion reports whether remote is a strictly newer semantic version
// than local — so a dev build ahead of the latest release never "updates"
// backwards, and any published newer release is detected automatically.
func newerVersion(remote, local string) bool {
	r, l := parseVer(remote), parseVer(local)
	for i := 0; i < 3; i++ {
		if r[i] != l[i] {
			return r[i] > l[i]
		}
	}
	return false
}

// CheckUpdate reports whether a newer release is published on GitHub. It works
// the same regardless of how backpack was installed (release or git clone) —
// the update itself always comes from the release assets.
func CheckUpdate() (bool, string, error) {
	tag, err := latestTag()
	if err != nil {
		return false, "", err
	}
	if !newerVersion(tag, app.Version) {
		return false, fmt.Sprintf("Already up to date (%s, latest release %s).", app.Version, tag), nil
	}
	return true, fmt.Sprintf("Version %s is available (current %s).", tag, app.Version), nil
}

// downloadAsset fetches the release tar.gz for this architecture into destDir
// and returns its path. Sources are tried in order.
func downloadAsset(tag, destDir string, logf func(string)) (string, error) {
	asset := fmt.Sprintf("backpack_linux_%s.tar.gz", runtime.GOARCH)
	url := fmt.Sprintf("%s/releases/download/%s/%s", repoURL(), tag, asset)

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", err
	}
	dest := filepath.Join(destDir, asset)

	var lastErr error = fmt.Errorf("no source reachable")
	for _, s := range sources(3 * time.Minute) {
		logf("Downloading " + asset + " via " + s.name + "...")
		resp, err := s.client.Get(s.prefix + url)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("%s returned status %d", s.name, resp.StatusCode)
			continue
		}
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
		if err != nil {
			resp.Body.Close()
			return "", err
		}
		_, err = io.Copy(f, resp.Body)
		resp.Body.Close()
		f.Close()
		if err != nil {
			lastErr = err
			continue
		}
		return dest, nil
	}
	return "", fmt.Errorf("could not download %s: %v", asset, lastErr)
}

// extractBinary pulls the `backpack` executable out of the release archive and
// atomically replaces the installed binary.
func extractBinary(archive string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("not a valid release archive: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != "backpack" {
			continue
		}
		// Write next to the target so the final rename is atomic.
		tmp := app.BinPath + ".new"
		out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			os.Remove(tmp)
			return err
		}
		out.Close()
		return os.Rename(tmp, app.BinPath)
	}
	return fmt.Errorf("no `backpack` binary found inside the archive")
}

// ApplyUpdate downloads the latest release and installs it safely:
// a full snapshot is taken first, the new binary is put in place, every service
// is restarted and health-checked, and if anything fails to come back up the
// snapshot is rolled back automatically — so a broken release can never leave
// the server without working tunnels.
func ApplyUpdate(logf func(string)) error {
	if logf == nil {
		logf = func(string) {}
	}
	tag, err := latestTag()
	if err != nil {
		return err
	}
	if !newerVersion(tag, app.Version) {
		logf("Already up to date (" + app.Version + ").")
		return nil
	}

	archive, err := downloadAsset(tag, app.InstallDir, logf)
	if err != nil {
		return err
	}

	// Snapshot BEFORE touching anything, so we can always get back.
	logf("Taking a safety snapshot...")
	snap, err := TakeSnapshot("pre-update")
	if err != nil {
		// A snapshot we cannot take is a good reason not to proceed blindly.
		return fmt.Errorf("could not take a safety snapshot: %w", err)
	}
	logf("Snapshot saved: " + filepath.Base(snap.Dir))

	logf("Installing " + tag + "...")
	if err := extractBinary(archive); err != nil {
		return err
	}

	// Keep the standard layout recorded for the uninstaller.
	_ = os.MkdirAll(app.BackupDir, 0755)
	if InstallPath() == "" {
		_ = os.MkdirAll(app.ConfigDir, 0755)
		_ = os.WriteFile(app.InstallPathFile, []byte(app.InstallDir+"\n"), 0644)
	}

	logf("Restarting services...")
	RestartService(app.WebUIService)
	ok, failed := RestartAll()
	logf(fmt.Sprintf("Restarted %d tunnels (%d failed).", ok, failed))

	// Health check: every tunnel that has a unit must come back active.
	logf("Checking health...")
	if bad := unhealthyAfterUpdate(); len(bad) > 0 {
		logf("Health check FAILED for: " + strings.Join(bad, ", "))
		logf("Rolling back to the previous version...")
		if rerr := RestoreSnapshot(snap, logf); rerr != nil {
			return fmt.Errorf("update failed AND rollback failed: %v (rollback: %v) — "+
				"restore manually from %s", strings.Join(bad, ", "), rerr, snap.Dir)
		}
		return fmt.Errorf("update to %s failed health check (%s) — rolled back to %s",
			tag, strings.Join(bad, ", "), snap.Meta.Version)
	}

	logf("Health check passed.")
	logf("Update complete — now running " + tag + ".")
	return nil
}

// unhealthyAfterUpdate returns the names of services that did not come back up
// after an update. It waits briefly, since systemd restarts are not instant.
func unhealthyAfterUpdate() []string {
	var bad []string
	if fileExists(app.ServiceDir+"/"+app.WebUIService) &&
		!WaitServiceActive(app.WebUIService, 20*time.Second) {
		bad = append(bad, "web panel")
	}
	for _, t := range List() {
		// Only judge tunnels that are supposed to be running.
		if !fileExists(app.ServiceDir + "/" + t.Service) {
			continue
		}
		if !WaitServiceActive(t.Service, 20*time.Second) {
			bad = append(bad, t.Name)
		}
	}
	return bad
}

// RollbackUpdate restores a snapshot on demand (menu: Update → Rollback).
func RollbackUpdate(s Snapshot, logf func(string)) error {
	return RestoreSnapshot(s, logf)
}
