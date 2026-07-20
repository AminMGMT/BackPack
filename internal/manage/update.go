package manage

// Release-based updater. Backpack updates itself from GitHub release assets
// (backpack_linux_amd64.tar.gz / backpack_linux_arm64.tar.gz). Every network
// step is tried in order:
//
//  1. direct GitHub
//  2. the tunnel SOCKS relay (the peer/kharej side can reach GitHub)
//
// A machine that has neither installs offline instead; see the README.

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
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

// Downloads go direct to GitHub, or through the tunnel relay when this machine
// cannot reach it. Both terminate TLS at github.com, so the checksum published
// with a release is worth checking against.
//
// Third-party GitHub proxies used to sit behind those two. They were removed:
// the archive and its SHA256SUMS travelled the same proxy, so a proxy that
// served a modified binary could serve a matching checksum with it, and the
// verification proved nothing precisely when it mattered — a blocked network is
// the only time a proxy gets used. A server with neither direct access nor a
// live tunnel now installs offline instead; see the README.

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
	matches, _ := filepath.Glob(app.ConfigDir + "/*.toml")
	for _, path := range matches {
		var cfg config.Config
		if _, err := toml.DecodeFile(path, &cfg); err != nil || cfg.Server.BindAddr == "" {
			continue
		}
		if port := relayExposedPort(cfg.Server.Ports, cfg.Server.Token); port != "" {
			return socks.HTTPClient("127.0.0.1:"+port, "backpack", cfg.Server.Token, timeout)
		}
	}
	return nil
}

// relayExposedPort finds the local port that a tunnel maps to the peer's SOCKS
// proxy, or "" when the tunnel carries no such mapping.
//
// Two forms exist: the legacy fixed 1080, and the port derived from the tunnel
// token. Matching only the legacy one — which is what this used to do — meant
// no relay was found for anything written since the port became token-derived,
// leaving the updater with nothing but a direct connection to GitHub. That is
// exactly what a server in Iran does not have.
func relayExposedPort(ports []string, token string) string {
	for _, suffix := range []string{
		fmt.Sprintf("=127.0.0.1:%d", app.SocksInternalPort),
		fmt.Sprintf("=127.0.0.1:%d", app.SocksPortForToken(token)),
	} {
		for _, p := range ports {
			p = strings.TrimSpace(p)
			if !strings.HasSuffix(p, suffix) {
				continue
			}
			port := strings.TrimSuffix(p, suffix)
			// The mapping may carry a bind address ("127.0.0.1:41234=...").
			if i := strings.LastIndex(port, ":"); i >= 0 {
				port = port[i+1:]
			}
			return port
		}
	}
	return ""
}

// source is one way to reach GitHub: a name to log and the client to use.
type source struct {
	name   string
	client *http.Client
}

// sources returns the ordered download paths: direct, then the tunnel relay.
func sources(timeout time.Duration) []source {
	out := []source{{name: "direct", client: &http.Client{Timeout: timeout}}}
	if relay := relayHTTPClient(timeout); relay != nil {
		out = append(out, source{name: "tunnel relay", client: relay})
	}
	return out
}

// tagNameRe pulls "tag_name":"v1.3.0" out of the GitHub API JSON.
var tagNameRe = regexp.MustCompile(`"tag_name"\s*:\s*"([^"]+)"`)

// versionValidRe sanity-checks a version string read from the raw VERSION file.
var versionValidRe = regexp.MustCompile(`^v?[0-9]+(\.[0-9]+){0,3}$`)

// latestTag discovers the newest published version. It tries two methods across
// both sources (direct, then the tunnel relay) so it works from Iran too:
//
//  1. the GitHub API releases/latest endpoint (JSON tag_name) — the accurate
//     "latest release", used when the server or its tunnel peer can reach
//     api.github.com directly (own IP, not rate limited).
//  2. the raw VERSION file on main, which is bumped with each release. This is
//     the fallback for a peer whose IP is rate limited by the API, where the
//     JSON request comes back 403 but raw.githubusercontent.com still answers.
func latestTag() (string, error) {
	var lastErr error = fmt.Errorf("no source reachable")
	beta := Channel() == ChannelBeta

	// On the beta channel the full release list is needed: /releases/latest
	// deliberately skips pre-releases, which are the whole point of the channel.
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", app.RepoOwner, app.RepoName)
	if beta {
		apiURL = fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=20", app.RepoOwner, app.RepoName)
	}
	rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/VERSION", app.RepoOwner, app.RepoName)

	// 1) GitHub API JSON — accurate, works direct and via the tunnel relay.
	for _, s := range sources(20 * time.Second) {
		resp, err := s.client.Get(apiURL)
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			if tag := pickTag(string(body), beta); tag != "" {
				return tag, nil
			}
		}
		lastErr = fmt.Errorf("api via %s: status %d", s.name, resp.StatusCode)
	}

	// 2) raw VERSION file — works when the API rate-limits the source's IP.
	for _, s := range sources(20 * time.Second) {
		resp, err := s.client.Get(rawURL)
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			// The VERSION file tracks the stable line, so it is only a valid
			// answer on the stable channel.
			if v := strings.TrimSpace(string(body)); versionValidRe.MatchString(v) && !beta {
				return v, nil
			}
		}
		lastErr = fmt.Errorf("VERSION via %s: status %d", s.name, resp.StatusCode)
	}
	return "", fmt.Errorf("could not reach GitHub (direct or through the tunnel relay): %v", lastErr)
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

// pickTag chooses a version from a GitHub releases response.
//
// On stable it takes the first tag and rejects it if it is a pre-release. On
// beta it walks every tag in the list — GitHub returns them newest first — and
// takes the highest version, so a pre-release newer than the last stable one
// wins while an older one does not.
func pickTag(body string, beta bool) string {
	matches := tagNameRe.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return ""
	}
	if !beta {
		tag := strings.TrimSpace(matches[0][1])
		if isPrerelease(tag) {
			return "" // /releases/latest should never return one, but do not install it if it does
		}
		return tag
	}

	best := ""
	for _, m := range matches {
		tag := strings.TrimSpace(m[1])
		if tag == "" {
			continue
		}
		if best == "" || newerVersion(tag, best) {
			best = tag
		}
	}
	return best
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
		resp, err := s.client.Get(url)
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

// fetchChecksums downloads the SHA256SUMS published with a release and returns
// the expected hash for one asset. An empty hash with no error means the
// release simply does not publish checksums.
func fetchChecksums(tag, asset string) (string, error) {
	url := fmt.Sprintf("%s/releases/download/%s/SHA256SUMS", repoURL(), tag)

	var lastErr error = fmt.Errorf("no source reachable")
	for _, s := range sources(30 * time.Second) {
		resp, err := s.client.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			return "", nil // this release predates published checksums
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("%s returned status %d", s.name, resp.StatusCode)
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		return hashFor(string(body), asset), nil
	}
	return "", lastErr
}

// hashFor picks one asset's hash out of a SHA256SUMS file. The format is
// "<hash>  <name>", with an optional "*" marking a binary-mode entry.
func hashFor(sums, asset string) string {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == asset {
			return strings.ToLower(fields[0])
		}
	}
	return ""
}

// verifyChecksum confirms a downloaded file hashes to want.
func verifyChecksum(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch — expected %s, got %s", want, got)
	}
	return nil
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

	// Confirm the archive is the file the release actually publishes before it
	// replaces the binary that runs every tunnel on this machine.
	//
	// This refuses rather than warns. A warning here is worth very little: the
	// caller that matters most is the web panel, which drives the update from a
	// browser where a logged line is easy to miss, and the thing being installed
	// runs as root. Declining to install is recoverable — the offline install in
	// the README always works — while installing an archive nobody vouched for
	// is not.
	want, cerr := fetchChecksums(tag, filepath.Base(archive))
	if cerr != nil {
		os.Remove(archive)
		return fmt.Errorf("could not fetch the checksum list for %s, so the download cannot be "+
			"verified: %w\nInstall offline instead — see the README", tag, cerr)
	}
	if want == "" {
		os.Remove(archive)
		return fmt.Errorf("release %s publishes no checksum for %s, so the download cannot be "+
			"verified\nInstall offline instead — see the README", tag, filepath.Base(archive))
	}
	if verr := verifyChecksum(archive, want); verr != nil {
		os.Remove(archive)
		return fmt.Errorf("the downloaded release failed verification: %w", verr)
	}
	logf("Checksum verified.")

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
	// Installs from before the monitor service acquire it here, so upgrading
	// does not leave a machine with no watchdog and no alerts. This restarts
	// unconditionally: the unit text is identical across versions, so an
	// install-if-missing check would decide there was nothing to do and leave
	// the old binary running.
	if err := RestartMonitorService(); err != nil {
		logf("Warning: monitor service could not start: " + err.Error())
	}
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
	// The monitor counts: if the new version cannot run the watchdog and the
	// alerts, the update has broken something even though every tunnel is still
	// carrying traffic — and without this it would be judged healthy and kept.
	if fileExists(app.ServiceDir+"/"+app.MonitorService) &&
		!WaitServiceActive(app.MonitorService, 20*time.Second) {
		bad = append(bad, "monitor")
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
