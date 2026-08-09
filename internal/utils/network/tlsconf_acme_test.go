package network

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

func TestACMERegistrySharesOneHTTPResponder(t *testing.T) {
	r := newACMERegistry("127.0.0.1:0")
	first := TLSSettings{ACMEDomain: "one.example", ACMEEmail: "ops@example.com", ACMECacheDir: t.TempDir()}
	second := TLSSettings{ACMEDomain: "two.example", ACMEEmail: "ops@example.com", ACMECacheDir: t.TempDir()}

	_, releaseFirst, err := r.acquire(first, t.Logf)
	if err != nil {
		t.Fatalf("register first domain: %v", err)
	}
	r.mu.Lock()
	server := r.server
	addr := r.listener.Addr().String()
	r.mu.Unlock()

	_, releaseSecond, err := r.acquire(second, t.Logf)
	if err != nil {
		t.Fatalf("register second domain: %v", err)
	}
	r.mu.Lock()
	if r.server != server {
		t.Fatal("second domain started another HTTP-01 server")
	}
	if got := len(r.entries); got != 2 {
		t.Fatalf("registered domains = %d, want 2", got)
	}
	r.mu.Unlock()

	unknown := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://unknown.example/.well-known/acme-challenge/test", nil)
	r.ServeHTTP(unknown, req)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown ACME host status = %d, want 404", unknown.Code)
	}

	releaseFirst()
	r.mu.Lock()
	if r.server == nil || r.total != 1 {
		t.Fatal("releasing one domain stopped the shared responder")
	}
	r.mu.Unlock()

	releaseSecond()
	r.mu.Lock()
	if r.server != nil || r.total != 0 || len(r.entries) != 0 {
		t.Fatal("last release did not clear the ACME registry")
	}
	r.mu.Unlock()

	conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatal("HTTP-01 listener remained open after its last lease closed")
	}
}

func TestACMERegistrySharesManagerForSameDomain(t *testing.T) {
	r := newACMERegistry("127.0.0.1:0")
	settings := TLSSettings{ACMEDomain: "Shared.Example.", ACMEEmail: "ops@example.com", ACMECacheDir: t.TempDir()}

	first, releaseFirst, err := r.acquire(settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, releaseSecond, err := r.acquire(settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("same domain and account settings created two autocert managers")
	}

	releaseFirst()
	releaseFirst() // release functions are intentionally idempotent
	r.mu.Lock()
	if r.total != 1 {
		t.Fatalf("idempotent release left %d leases, want 1", r.total)
	}
	r.mu.Unlock()
	releaseSecond()
}

func TestACMERegistryRejectsConflictingAccountSettings(t *testing.T) {
	r := newACMERegistry("127.0.0.1:0")
	settings := TLSSettings{ACMEDomain: "conflict.example", ACMEEmail: "one@example.com", ACMECacheDir: t.TempDir()}
	_, release, err := r.acquire(settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	conflict := settings
	conflict.ACMEEmail = "two@example.com"
	if _, _, err := r.acquire(conflict, nil); err == nil {
		t.Fatal("same domain was accepted with conflicting ACME account settings")
	}
}

func TestTLSLeaseCloseIsIdempotent(t *testing.T) {
	var calls atomic.Int32
	lease := &TLSLease{onClose: func() { calls.Add(1) }}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = lease.Close()
		}()
	}
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("lease release called %d times, want once", got)
	}
}

func TestACMEResponderServesSharedCacheAndHandsOffAcrossProcesses(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()

	cacheDir := t.TempDir()
	owner := newACMERegistry(addr)
	standby := newACMERegistry(addr)
	standby.retryEvery = 10 * time.Millisecond
	one := TLSSettings{ACMEDomain: "one.example", ACMECacheDir: cacheDir}
	two := TLSSettings{ACMEDomain: "two.example", ACMECacheDir: cacheDir}

	_, releaseOwner, err := owner.acquire(one, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	_, releaseStandby, err := standby.acquire(two, t.Logf)
	if err != nil {
		releaseOwner()
		t.Fatal(err)
	}
	defer releaseStandby()

	standby.mu.Lock()
	if standby.server != nil || standby.retryStop == nil {
		standby.mu.Unlock()
		releaseOwner()
		t.Fatal("second process neither waited for nor unexpectedly acquired the occupied port")
	}
	standby.mu.Unlock()

	const token = "cross-process-token"
	const response = "cross-process-token.key-authorization"
	cache := autocert.DirCache(cacheDir)
	if err := cache.Put(context.Background(), token+"+http-01", []byte(response)); err != nil {
		releaseOwner()
		t.Fatalf("write shared challenge token: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/.well-known/acme-challenge/"+token, nil)
	if err != nil {
		releaseOwner()
		t.Fatal(err)
	}
	req.Host = "two.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		releaseOwner()
		t.Fatalf("request challenge through the owner process: %v", err)
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		releaseOwner()
		t.Fatal(readErr)
	}
	if string(body) != response {
		releaseOwner()
		t.Fatalf("shared-cache challenge response = %q, want %q", body, response)
	}

	releaseOwner()
	deadline := time.Now().Add(time.Second)
	for {
		standby.mu.Lock()
		acquired := standby.server != nil
		standby.mu.Unlock()
		if acquired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("standby process did not acquire port 80 after the owner released it")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
