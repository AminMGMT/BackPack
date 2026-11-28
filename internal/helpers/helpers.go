// Package helpers installs the pinned protocol helpers (Xray, NaiveProxy and
// sing-box) the managed HTTPS carriers run. They are the releases and
// checksums install.sh pins, in the same versioned directories, so a server
// installed without BP_HELPERS gets them the first time a tunnel needs one:
// at setup, from a setup link, or when the tunnel starts.
package helpers

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/app"
)

// Root is where install.sh and Install put every helper.
const Root = "/usr/local/lib/backpack/helpers"

type archFile struct {
	asset, sha256 string
	stem          string // the archive's top directory, "" for none
}

type pin struct {
	repo, version string
	files         map[string]archFile // GOARCH → release asset
	members       []string            // the tool first, then files it loads
	glibc         bool
}

var pins = map[string]pin{
	"xray": {
		repo: "XTLS/Xray-core", version: "v26.3.27", members: []string{"xray"},
		files: map[string]archFile{
			"amd64": {asset: "Xray-linux-64.zip", sha256: "23cd9af937744d97776ee35ecad4972cf4b2109d1e0fe6be9930467608f7c8ae"},
			"arm64": {asset: "Xray-linux-arm64-v8a.zip", sha256: "4d30283ae614e3057f730f67cd088a42be6fdf91f8639d82cb69e48cde80413c"},
		},
	},
	"naive": {
		repo: "klzgrad/naiveproxy", version: "v154.0.8037.49-5", members: []string{"naive"}, glibc: true,
		files: map[string]archFile{
			"amd64": {asset: "naiveproxy-v154.0.8037.49-5-linux-x64.tar.xz", stem: "naiveproxy-v154.0.8037.49-5-linux-x64",
				sha256: "80214dfea1767f6ecf63a3a79dc9f48b203460f51abc609357fff48dd5844c7d"},
			"arm64": {asset: "naiveproxy-v154.0.8037.49-5-linux-arm64.tar.xz", stem: "naiveproxy-v154.0.8037.49-5-linux-arm64",
				sha256: "8f4d917fa39a5d8cf1c17fc12ffd99ba84c015b62b43461d09cc074ca4114d8d"},
		},
	},
	"sing-box": {
		repo: "SagerNet/sing-box", version: "v1.14.2", members: []string{"sing-box", "libcronet.so"},
		files: map[string]archFile{
			"amd64": {asset: "sing-box-1.14.2-linux-amd64.tar.gz", stem: "sing-box-1.14.2-linux-amd64",
				sha256: "a684484d7477d1437282ee411f4d131d0340aaad60a7868841ebd5d87dd8a0c6"},
			"arm64": {asset: "sing-box-1.14.2-linux-arm64.tar.gz", stem: "sing-box-1.14.2-linux-arm64",
				sha256: "b43a1fb1bda131c6653576741ce527eb2bdeab7c9308ca90ee8b972abb7e4a7f"},
		},
	},
}

// What Install reaches outside itself for, so a test can serve the archive
// and install into a temporary directory.
var (
	root         = Root
	arch         = runtime.GOARCH
	downloadBase = "https://github.com"
	archiveDirs  = func() []string {
		dirs := []string{"/root/BackPack", "/root"}
		if b, err := os.ReadFile(app.InstallPathFile); err == nil {
			if dir := strings.TrimSpace(string(b)); dir != "" {
				dirs = append([]string{dir}, dirs...)
			}
		}
		return dirs
	}
	glibcLoaders = map[string]string{"amd64": "/lib64/ld-linux-x86-64.so.2", "arm64": "/lib/ld-linux-aarch64.so.1"}
)

// Path is where tool is installed: the path tunnels are configured with.
func Path(tool string) string {
	return filepath.Join(root, tool, pins[tool].version+"-"+arch, tool)
}

// Version is the pinned release of tool.
func Version(tool string) string { return pins[tool].version }

// Pinned reports tool's release asset and checksum for this architecture.
func Pinned(tool, goarch string) (repo, version, asset, sha string) {
	p := pins[tool]
	f := p.files[goarch]
	return p.repo, p.version, f.asset, f.sha256
}

// Source is one way to reach GitHub.
type Source struct {
	Name   string
	Client *http.Client
}

// Direct reaches GitHub without a relay.
func Direct(timeout time.Duration) []Source {
	return []Source{{Name: "direct", Client: &http.Client{Timeout: timeout}}}
}

// Executable reports whether path is a runnable file.
func Executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0
}

var mu sync.Mutex

// Install puts tool's pinned release at Path(tool) and returns that path.
// It takes the archive from this server first (an operator without GitHub
// copies it next to Backpack), then from GitHub through each source, and
// refuses it unless it matches the pinned checksum. Nothing reaches the
// helper directory until the archive has been verified and unpacked.
func Install(tool string, sources []Source, logf func(string)) (string, error) {
	if logf == nil {
		logf = func(string) {}
	}
	mu.Lock()
	defer mu.Unlock()
	p, ok := pins[tool]
	if !ok {
		return "", fmt.Errorf("%s is not a helper Backpack installs", tool)
	}
	f := p.files[arch]
	if runtime.GOOS != "linux" || f.asset == "" {
		return "", fmt.Errorf("the pinned %s helper is built for Linux amd64 and arm64 only; install one by hand", tool)
	}
	final := Path(tool)
	dir := filepath.Dir(final)
	if Executable(final) {
		return final, nil
	}
	if p.glibc {
		if _, err := os.Stat(glibcLoaders[arch]); err != nil {
			return "", fmt.Errorf("the pinned %s helper needs glibc, which this server does not have; install a suitable binary by hand", tool)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0755); err != nil {
		return "", fmt.Errorf("installing the %s helper needs root: %w", tool, err)
	}
	stage, err := os.MkdirTemp(filepath.Dir(dir), ".stage-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)

	archive := filepath.Join(stage, f.asset)
	if err := fetch(p, f, archive, sources, logf); err != nil {
		return "", err
	}
	payload := filepath.Join(stage, "payload")
	if err := os.Mkdir(payload, 0755); err != nil {
		return "", err
	}
	if err := unpack(archive, f, p.members, payload); err != nil {
		return "", fmt.Errorf("unpacking %s: %w", f.asset, err)
	}
	if err := os.Rename(payload, dir); err != nil {
		if Executable(final) {
			return final, nil
		}
		return "", err
	}
	logf("Installed The " + display(tool) + " Helper (" + p.version + "): " + final)
	return final, nil
}

// fetch puts the verified archive at dest.
func fetch(p pin, f archFile, dest string, sources []Source, logf func(string)) error {
	local := localCandidates(f.asset)
	for _, path := range local {
		if err := copyVerified(path, f.sha256, dest); err == nil {
			logf("Using " + path)
			return nil
		}
	}
	url := fmt.Sprintf("%s/%s/releases/download/%s/%s", downloadBase, p.repo, p.version, f.asset)
	var lastErr error = errors.New("no source reachable")
	for _, s := range sources {
		logf("Downloading " + f.asset + " via " + s.Name + "...")
		if err := downloadVerified(s.Client, url, f.sha256, dest); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	where := "this server"
	if len(local) > 0 {
		where = filepath.Dir(local[0])
	}
	return fmt.Errorf("could not download %s (%v); copy it from %s to %s and try again", f.asset, lastErr, url, where)
}

func localCandidates(asset string) []string {
	var out []string
	seen := map[string]bool{}
	for _, dir := range archiveDirs() {
		if dir == "" {
			continue
		}
		path := filepath.Join(dir, asset)
		if !seen[path] {
			seen[path] = true
			out = append(out, path)
		}
	}
	return out
}

const maxArchive = 256 << 20

func downloadVerified(client *http.Client, url, want, dest string) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return writeVerified(resp.Body, want, dest)
}

func copyVerified(src, want, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	return writeVerified(f, want, dest)
}

// writeVerified keeps dest only when what was written hashes to want.
func writeVerified(r io.Reader, want, dest string) error {
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, maxArchive+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxArchive {
		err = errors.New("archive is larger than any helper release")
	}
	if err == nil && !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want) {
		err = errors.New("CHECKSUM MISMATCH: refused")
	}
	if err != nil {
		os.Remove(dest)
	}
	return err
}

// unpack writes each member of the verified archive into dir, the tool
// executable and the rest (libcronet.so) readable.
func unpack(archive string, f archFile, members []string, dir string) error {
	want := map[string]string{}
	for _, m := range members {
		name := m
		if f.stem != "" {
			name = f.stem + "/" + m
		}
		want[name] = m
	}
	found := map[string]bool{}
	write := func(name string, r io.Reader) error {
		m, ok := want[name]
		if !ok || found[m] {
			return nil
		}
		mode := os.FileMode(0644)
		if m == members[0] {
			mode = 0755
		}
		out, err := os.OpenFile(filepath.Join(dir, m), os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		n, err := io.Copy(out, io.LimitReader(r, maxArchive))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err == nil && n == 0 {
			err = fmt.Errorf("%s is empty", m)
		}
		found[m] = true
		return err
	}

	switch {
	case strings.HasSuffix(f.asset, ".zip"):
		z, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer z.Close()
		for _, e := range z.File {
			if !e.Mode().IsRegular() {
				continue
			}
			r, err := e.Open()
			if err != nil {
				return err
			}
			err = write(e.Name, r)
			r.Close()
			if err != nil {
				return err
			}
		}
	default:
		r, wait, err := decompress(archive, f.asset)
		if err != nil {
			return err
		}
		tr := tar.NewReader(r)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				wait()
				return err
			}
			if h.Typeflag != tar.TypeReg {
				continue
			}
			if err := write(h.Name, tr); err != nil {
				wait()
				return err
			}
		}
		if err := wait(); err != nil {
			return err
		}
	}
	for _, m := range members {
		if !found[m] {
			return fmt.Errorf("%s is not in the archive", m)
		}
	}
	return nil
}

// decompress opens a .tar.gz with Go, or a .tar.xz with the system's xz:
// the archive has already matched its pinned checksum.
func decompress(archive, asset string) (io.Reader, func() error, error) {
	file, err := os.Open(archive)
	if err != nil {
		return nil, nil, err
	}
	if strings.HasSuffix(asset, ".tar.gz") {
		gz, err := gzip.NewReader(file)
		if err != nil {
			file.Close()
			return nil, nil, err
		}
		return gz, func() error { gz.Close(); return file.Close() }, nil
	}
	file.Close()
	xz, err := exec.LookPath("xz")
	if err != nil {
		return nil, nil, errors.New("xz is needed to unpack it; install xz-utils (apt install xz-utils) and try again")
	}
	cmd := exec.Command(xz, "-dc", archive)
	cmd.Env = SystemEnv()
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return out, func() error { io.Copy(io.Discard, out); return cmd.Wait() }, nil
}

// SystemEnv is this process's environment without LD_LIBRARY_PATH, so a
// system tool loads the system's own libraries.
func SystemEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "LD_LIBRARY_PATH=") {
			env = append(env, kv)
		}
	}
	return env
}

// ToolAt names the pinned helper whose installed path is binary, or "".
func ToolAt(binary string) string {
	for tool := range pins {
		if filepath.Clean(binary) == Path(tool) {
			return tool
		}
	}
	return ""
}

var (
	lastTry  = map[string]time.Time{}
	lastErr  = map[string]error{}
	tryMu    sync.Mutex
	retryGap = 5 * time.Minute
)

// EnsureAt installs the pinned helper a tunnel is configured to run at
// binary when it is missing, as the tunnel starts. A path of the operator's
// own is left alone. A failed install is tried again only after a pause,
// so a tunnel retrying its start every second does not download each time.
func EnsureAt(binary string, logf func(string)) error {
	tool := ToolAt(binary)
	if tool == "" || Executable(binary) {
		return nil
	}
	tryMu.Lock()
	if t, ok := lastTry[tool]; ok && time.Since(t) < retryGap {
		err := lastErr[tool]
		tryMu.Unlock()
		return err
	}
	lastTry[tool] = time.Now()
	tryMu.Unlock()
	if logf != nil {
		logf("the " + display(tool) + " helper is missing at " + binary + "; installing the pinned " + pins[tool].version)
	}
	_, err := Install(tool, Direct(3*time.Minute), logf)
	tryMu.Lock()
	lastErr[tool] = err
	if err == nil {
		delete(lastTry, tool)
	}
	tryMu.Unlock()
	return err
}

func display(tool string) string {
	switch tool {
	case "xray":
		return "Xray"
	case "naive":
		return "NaiveProxy"
	case "sing-box":
		return "Sing-Box"
	}
	return tool
}
