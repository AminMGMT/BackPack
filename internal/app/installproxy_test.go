package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise Go's actual fallback behavior with local proxies. Comma-separated
// defaults stop at 403/500, even though another mirror has the module.
func TestInstallerModuleProxyFallback(t *testing.T) {
	src, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	assignment := strings.TrimSpace(regexp.MustCompile(`(?m)^\s*export GOPROXY=.*$`).FindString(string(src)))
	if assignment == "" {
		t.Fatal("installer module proxy assignment missing")
	}
	for _, tc := range []struct {
		name        string
		status      int
		override    string
		wantSuccess bool
		wantBlocked bool
	}{
		{"forbidden", 403, "", true, true},
		{"outage", 500, "", true, true},
		{"custom-proxy", 403, "good", true, false},
		{"custom-comma-policy", 403, "blocked,good", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var blockedCalls, goodCalls atomic.Int32
			blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				blockedCalls.Add(1)
				w.WriteHeader(tc.status)
			}))
			defer blocked.Close()
			good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				goodCalls.Add(1)
				switch {
				case strings.HasSuffix(r.URL.Path, ".info"):
					_, _ = w.Write([]byte(`{"Version":"v1.0.0","Time":"2026-01-01T00:00:00Z"}`))
				case strings.HasSuffix(r.URL.Path, ".mod"):
					_, _ = w.Write([]byte("module example.com/installer-probe\ngo 1.20\n"))
				default:
					http.NotFound(w, r)
				}
			}))
			defer good.Close()
			block := strings.NewReplacer("https://proxy.golang.org", blocked.URL,
				"https://mirror-go.runflare.com", good.URL, "https://goproxy.cn", good.URL).Replace(assignment)
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module installer-test\ngo 1.20\n"), 0600); err != nil {
				t.Fatal(err)
			}
			env := []string{}
			for _, e := range os.Environ() {
				key, _, _ := strings.Cut(e, "=")
				switch strings.ToUpper(key) {
				case "GOPROXY", "GOMODCACHE", "GOSUMDB", "GOTOOLCHAIN", "GOPRIVATE", "GONOPROXY", "GOFLAGS", "GOWORK", "GOENV":
					continue
				}
				env = append(env, e)
			}
			if tc.override != "" {
				env = append(env, "GOPROXY="+strings.NewReplacer("blocked", blocked.URL, "good", good.URL).Replace(tc.override))
			}
			env = append(env, "GOMODCACHE="+filepath.Join(dir, "cache"), "GOSUMDB=off", "GOTOOLCHAIN=local", "GOWORK=off", "GOENV=off")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", "-c", block+"\ngo list -m example.com/installer-probe@v1.0.0")
			cmd.Dir, cmd.Env = dir, env
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.wantSuccess {
				t.Fatalf("success=%v, want %v: %s (%v)", err == nil, tc.wantSuccess, out, err)
			}
			if (blockedCalls.Load() > 0) != tc.wantBlocked || (goodCalls.Load() > 0) != tc.wantSuccess {
				t.Fatalf("unexpected proxy requests: blocked=%d good=%d", blockedCalls.Load(), goodCalls.Load())
			}
		})
	}
}
