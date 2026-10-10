package shorthttps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef"

func fixture(t *testing.T, target string, wrap func(http.Handler) http.Handler) (*Server, *Client, context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s, err := NewServer(ctx, target, testToken)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	var handler http.Handler = s
	if wrap != nil {
		handler = wrap(handler)
	}
	h := httptest.NewTLSServer(handler)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: h.Certificate().Raw})
	c, err := NewClient(h.URL, testToken, string(ca))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); s.Close(); h.Close() })
	return s, c, ctx, cancel
}

func TestRetryKeepsOneTCPStreamAndPreservesDelayedHalfCloseReply(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	payload := bytes.Repeat([]byte("real TCP data\x00"), 270)
	result := make(chan []byte, 1)
	go func() {
		conn, err := backend.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		data, _ := io.ReadAll(conn)
		result <- data
		time.Sleep(30 * time.Millisecond)
		sum := sha256.Sum256(data)
		_, _ = conn.Write(sum[:])
		_ = conn.(*net.TCPConn).CloseWrite()
	}()
	var dropped atomic.Bool
	var connections atomic.Int32
	s, c, ctx, _ := fixture(t, backend.Addr().String(), func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			connections.Add(1)
			body, _ := io.ReadAll(r.Body)
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
			var q request
			_ = json.Unmarshal(body, &q)
			record := httptest.NewRecorder()
			next.ServeHTTP(record, r)
			if len(q.Data) > 0 && dropped.CompareAndSwap(false, true) {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					conn.Close()
				}
				return
			}
			for k, values := range record.Header() {
				for _, v := range values {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(record.Code)
			_, _ = w.Write(record.Body.Bytes())
		})
	})
	local, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := local.Accept()
		if err != nil {
			done <- err
			return
		}
		done <- c.Forward(ctx, conn)
	}()
	app, err := net.Dial("tcp", local.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := writeAll(app, payload); err != nil {
		t.Fatal(err)
	}
	app.(*net.TCPConn).CloseWrite()
	reply, err := io.ReadAll(app)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(payload)
	if !bytes.Equal(reply, want[:]) {
		t.Fatal("delayed backend reply was lost or duplicated")
	}
	if got := <-result; !bytes.Equal(got, payload) {
		t.Fatal("lost-response retry duplicated or corrupted backend bytes")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !dropped.Load() || connections.Load() < 4 {
		t.Fatal("did not exercise a dropped data response and fresh requests")
	}
	s.mu.Lock()
	remaining := len(s.sessions)
	s.mu.Unlock()
	if remaining != 0 {
		t.Fatal("closed stream leaked a backend session")
	}
}

func TestRelayRejectsAuthenticationInvalidOffsetsAndDestinations(t *testing.T) {
	for _, target := range []string{"example.com:80", "192.0.2.1:443", "127.0.0.1:0", "127.0.0.1:65536"} {
		if s, err := NewServer(context.Background(), target, testToken); err == nil {
			s.Close()
			t.Fatalf("accepted non-local/invalid destination %s", target)
		}
	}
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	s, c, ctx, _ := fixture(t, backend.Addr().String(), nil)
	wrong := *c
	wrong.token = "incorrect"
	if _, err := wrong.exchange(ctx, request{ID: strings.Repeat("a", 32), Open: true}); err == nil {
		t.Fatal("wrong token accepted")
	}
	s.mu.Lock()
	n := len(s.sessions)
	s.mu.Unlock()
	if n != 0 {
		t.Fatal("unauthorized request opened a backend")
	}
	q := request{ID: strings.Repeat("b", 32), Open: true}
	if _, err := c.exchange(ctx, q); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []request{{ID: q.ID, Offset: 1}, {ID: q.ID, Ack: 1}, {ID: q.ID, Data: make([]byte, chunkSize+1)}, {ID: "bad", Open: true}} {
		if _, err := c.exchange(ctx, bad); err == nil {
			t.Fatal("invalid offset or oversized request accepted")
		}
	}
	_, _ = c.exchange(ctx, request{ID: q.ID, Close: true})
}

func TestClientVerifiesCertificateAndCancellationStopsActiveStreams(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		conn, err := backend.Accept()
		if err == nil {
			defer conn.Close()
			_, _ = io.Copy(io.Discard, conn)
		}
	}()
	_, c, ctx, cancel := fixture(t, backend.Addr().String(), nil)
	wrong := *c
	wrong.endpoint = strings.Replace(c.endpoint, "127.0.0.1", "localhost", 1)
	_, verifyErr := wrong.exchange(ctx, request{ID: strings.Repeat("c", 32), Open: true})
	var verification *tls.CertificateVerificationError
	if !errors.As(verifyErr, &verification) {
		t.Fatalf("wrong hostname was not rejected by certificate verification: %v", verifyErr)
	}
	local, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	done := make(chan error, 1)
	go func() { done <- c.Serve(ctx, local, nil) }()
	app, err := net.Dial("tcp", local.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	_, _ = app.Write([]byte("pending"))
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled relay retained active local streams")
	}
}

func TestBackendResetDoesNotBecomeSuccessfulEOF(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		conn, err := backend.Accept()
		if err == nil {
			var b [1]byte
			_, _ = conn.Read(b[:])
			conn.(*net.TCPConn).SetLinger(0)
			conn.Close()
		}
	}()
	_, c, ctx, _ := fixture(t, backend.Addr().String(), nil)
	local, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := local.Accept()
		if err != nil {
			done <- err
			return
		}
		done <- c.Forward(ctx, conn)
	}()
	app, err := net.Dial("tcp", local.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = app.Write([]byte("request"))
	_, readErr := io.ReadAll(app)
	if readErr == nil {
		t.Fatal("backend reset was reported as a graceful EOF")
	}
	if timed, ok := readErr.(net.Error); ok && timed.Timeout() {
		t.Fatal("backend reset was not propagated promptly")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("failed backend stream passed")
		}
	case <-time.After(time.Second):
		t.Fatal("failed backend stream leaked")
	}
}
