package helpers

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The helpers Backpack installs are the ones install.sh installs: same
// releases, same checksums, same places.
func TestPinsMatchTheInstaller(t *testing.T) {
	raw, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for tool, p := range pins {
		if !strings.Contains(script, "helper_version="+p.version+"; helper_repo="+p.repo) {
			t.Errorf("install.sh does not pin %s %s", p.repo, p.version)
		}
		for goarch, f := range p.files {
			m := regexp.MustCompile(regexp.QuoteMeta(tool+"/"+goarch) + `\) helper_sha=([0-9a-f]{64})(?:; helper_arch=(\S+))? ;;`).FindStringSubmatch(script)
			if m == nil || m[1] != f.sha256 {
				t.Errorf("%s/%s: install.sh pins %v, Backpack pins %s", tool, goarch, m, f.sha256)
				continue
			}
			a := goarch
			if m[2] != "" {
				a = m[2]
			}
			want := map[string]string{
				"xray":     "Xray-linux-" + a + ".zip",
				"naive":    "naiveproxy-" + p.version + "-linux-" + a + ".tar.xz",
				"sing-box": "sing-box-" + strings.TrimPrefix(p.version, "v") + "-linux-" + a + ".tar.gz",
			}[tool]
			if f.asset != want || (f.stem != "" && f.asset != f.stem+filepath.Ext(strings.TrimSuffix(f.asset, filepath.Ext(f.asset)))+filepath.Ext(f.asset)) {
				t.Errorf("%s/%s: asset %s (stem %s), install.sh fetches %s", tool, goarch, f.asset, f.stem, want)
			}
		}
	}
	if Path("xray") != "/usr/local/lib/backpack/helpers/xray/v26.3.27-"+arch+"/xray" {
		t.Errorf("Xray lives at %s", Path("xray"))
	}
}

func zipArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func tarArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gz(t *testing.T, raw []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(raw)
	w.Close()
	return buf.Bytes()
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// fake points the installer at a temporary root, serves archive for tool
// and pins its checksum.
func fake(t *testing.T, tool string, archive []byte, pinned string, dirs ...string) (base string) {
	t.Helper()
	prevPins, prevRoot, prevArch, prevBase, prevDirs, prevLoaders := pins, root, arch, downloadBase, archiveDirs, glibcLoaders
	t.Cleanup(func() {
		pins, root, arch, downloadBase, archiveDirs, glibcLoaders = prevPins, prevRoot, prevArch, prevBase, prevDirs, prevLoaders
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(archive) }))
	t.Cleanup(srv.Close)
	copied := map[string]pin{}
	for k, v := range prevPins {
		copied[k] = v
	}
	p := copied[tool]
	f := p.files["amd64"]
	f.sha256 = pinned
	p.files = map[string]archFile{"amd64": f}
	copied[tool] = p
	pins, root, arch, downloadBase = copied, t.TempDir(), "amd64", srv.URL
	archiveDirs = func() []string { return dirs }
	loader := filepath.Join(t.TempDir(), "ld.so")
	os.WriteFile(loader, nil, 0644)
	glibcLoaders = map[string]string{"amd64": loader}
	return srv.URL
}

func direct() []Source { return Direct(10 * time.Second) }

func TestXrayInstallsFromTheVerifiedRelease(t *testing.T) {
	archive := zipArchive(t, map[string]string{"LICENSE": "MPL-2.0", "xray": "#!/bin/sh\n"})
	fake(t, "xray", archive, sum(archive))
	var log []string
	got, err := Install("xray", direct(), func(l string) { log = append(log, l) })
	if err != nil {
		t.Fatal(err)
	}
	if got != Path("xray") || !Executable(got) {
		t.Fatalf("installed at %s", got)
	}
	if !strings.Contains(strings.Join(log, "\n"), "Installed The Xray Helper") {
		t.Errorf("not reported: %q", log)
	}
	if again, err := Install("xray", nil, nil); err != nil || again != got {
		t.Fatalf("an installed helper was not reused: %v", err)
	}
}

// sing-box comes with the library it loads, which is not made executable.
func TestSingBoxInstallsWithItsLibrary(t *testing.T) {
	stem := pins["sing-box"].files["amd64"].stem
	archive := gz(t, tarArchive(t, map[string]string{stem + "/sing-box": "bin", stem + "/libcronet.so": "lib", stem + "/LICENSE": "GPL"}))
	fake(t, "sing-box", archive, sum(archive))
	got, err := Install("sing-box", direct(), nil)
	if err != nil {
		t.Fatal(err)
	}
	lib, err := os.Stat(filepath.Join(filepath.Dir(got), "libcronet.so"))
	if err != nil || lib.Mode().Perm() != 0644 || !Executable(got) {
		t.Fatalf("sing-box %v, library %v %v", Executable(got), lib, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(got), "LICENSE")); err == nil {
		t.Fatal("a file the helper does not need was installed")
	}
}

func TestNaiveInstallsFromItsXzArchive(t *testing.T) {
	xz, err := exec.LookPath("xz")
	if err != nil {
		t.Skip("needs xz")
	}
	stem := pins["naive"].files["amd64"].stem
	cmd := exec.Command(xz, "-zc")
	cmd.Env = SystemEnv()
	cmd.Stdin = bytes.NewReader(tarArchive(t, map[string]string{stem + "/naive": "naive build", stem + "/USAGE.txt": "x"}))
	archive, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	fake(t, "naive", archive, sum(archive))
	got, err := Install("naive", direct(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(got); string(body) != "naive build" || !Executable(got) {
		t.Fatalf("installed %q", body)
	}

	glibcLoaders = map[string]string{"amd64": "/nonexistent/ld.so"}
	os.RemoveAll(filepath.Dir(got))
	if _, err := Install("naive", direct(), nil); err == nil || !strings.Contains(err.Error(), "glibc") {
		t.Fatalf("naive was installed without glibc: %v", err)
	}
}

func TestTheWrongChecksumIsNeverInstalled(t *testing.T) {
	archive := zipArchive(t, map[string]string{"xray": "not the pinned build"})
	fake(t, "xray", archive, strings.Repeat("0", 64))
	if _, err := Install("xray", direct(), nil); err == nil || !strings.Contains(err.Error(), "CHECKSUM MISMATCH") {
		t.Fatalf("a mismatched archive was not refused: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "xray")); len(entries) != 0 {
		t.Fatalf("a refused archive left %d entries", len(entries))
	}
}

// A server that cannot reach GitHub installs from the archive copied next
// to Backpack, checked against the same pin.
func TestAnArchiveCopiedToTheServerNeedsNoNetwork(t *testing.T) {
	archive := zipArchive(t, map[string]string{"xray": "offline build"})
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Xray-linux-64.zip"), archive, 0644)
	fake(t, "xray", archive, sum(archive), dir)
	if _, err := Install("xray", nil, nil); err != nil {
		t.Fatalf("the copied archive was not used: %v", err)
	}

	empty := t.TempDir()
	fake(t, "xray", archive, sum(archive), empty)
	_, err := Install("xray", []Source{{Name: "direct", Client: &http.Client{Transport: failing{}}}}, nil)
	if err == nil || !strings.Contains(err.Error(), empty) || !strings.Contains(err.Error(), "Xray-linux-64.zip") {
		t.Fatalf("an offline failure does not say where to put the archive: %v", err)
	}
}

type failing struct{}

func (failing) RoundTrip(*http.Request) (*http.Response, error) { return nil, errors.New("blocked") }

// A tunnel starting finds its missing helper installed; a path of the
// operator's own is never replaced, and a failure is not retried every second.
func TestATunnelStartingInstallsItsMissingHelper(t *testing.T) {
	archive := zipArchive(t, map[string]string{"xray": "#!/bin/sh\n"})
	fake(t, "xray", archive, sum(archive))
	own := filepath.Join(t.TempDir(), "xray")
	if err := EnsureAt(own, nil); err != nil || Executable(own) {
		t.Fatalf("a path of the operator's own was installed to: %v", err)
	}
	if err := EnsureAt(Path("xray"), nil); err != nil || !Executable(Path("xray")) {
		t.Fatalf("the missing helper was not installed: %v", err)
	}

	prevGap := retryGap
	defer func() { retryGap = prevGap }()
	retryGap = time.Hour
	fake(t, "xray", archive, strings.Repeat("0", 64))
	if err := EnsureAt(Path("xray"), nil); err == nil {
		t.Fatal("a mismatched archive was installed")
	}
	downloadBase = "http://127.0.0.1:1"
	pins["xray"].files["amd64"] = archFile{asset: "Xray-linux-64.zip", sha256: sum(archive)}
	if err := EnsureAt(Path("xray"), nil); err == nil || Executable(Path("xray")) {
		t.Fatal("a failed install was tried again at once")
	}
}

// The real pinned releases, fetched from GitHub: BP_HELPER_INSTALL_LIVE=1.
func TestTheRealPinnedReleasesInstall(t *testing.T) {
	if os.Getenv("BP_HELPER_INSTALL_LIVE") != "1" {
		t.Skip("downloads the helpers from GitHub; set BP_HELPER_INSTALL_LIVE=1")
	}
	prevRoot, prevDirs := root, archiveDirs
	defer func() { root, archiveDirs = prevRoot, prevDirs }()
	root, archiveDirs = t.TempDir(), func() []string { return nil }
	for tool, args := range map[string][]string{"xray": {"version"}, "sing-box": {"version"}, "naive": {"--version"}} {
		path, err := Install(tool, Direct(5*time.Minute), func(l string) { t.Log(l) })
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		out, err := exec.Command(path, args...).CombinedOutput()
		v := strings.TrimPrefix(strings.SplitN(pins[tool].version, "-", 2)[0], "v")
		if err != nil || !strings.Contains(string(out), v) {
			t.Fatalf("%s does not run as %s: %v\n%s", tool, v, err, out)
		}
	}
}
