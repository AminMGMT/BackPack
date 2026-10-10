// Package shorthttps is an experimental fixed-destination TCP relay over
// bounded HTTPS requests. Every request uses a new TCP connection; sequence
// offsets retain the logical stream when a response or connection is lost.
package shorthttps

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const chunkSize = 2048
const bufferLimit = 64 << 10
const maxSessions = 64

type request struct {
	ID     string `json:"id"`
	Open   bool   `json:"open,omitempty"`
	Offset uint64 `json:"offset"`
	Data   []byte `json:"data,omitempty"`
	EOF    bool   `json:"eof,omitempty"`
	Ack    uint64 `json:"ack"`
	Close  bool   `json:"close,omitempty"`
}

type response struct {
	Ack    uint64 `json:"ack"`
	Offset uint64 `json:"offset"`
	Data   []byte `json:"data,omitempty"`
	EOF    bool   `json:"eof,omitempty"`
}

type session struct {
	conn       net.Conn
	serial     sync.Mutex // one state transition per request, including retries
	mu         sync.Mutex
	upload     uint64
	sentEOF    bool
	download   uint64
	offered    uint64
	buffer     []byte
	readEOF    bool
	readFailed bool
	closed     bool
	last       time.Time
	ready      chan struct{}
	wake       chan struct{}
}

func (s *session) notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
func (s *session) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.conn.Close()
	s.notify(s.ready)
	s.notify(s.wake)
}

func (s *session) read(ctx context.Context) {
	defer func() { s.mu.Lock(); s.readEOF = true; s.mu.Unlock(); s.notify(s.ready) }()
	data := make([]byte, chunkSize)
	for {
		s.mu.Lock()
		full, closed := len(s.buffer) > bufferLimit-chunkSize, s.closed
		s.mu.Unlock()
		if closed {
			return
		}
		if full {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			}
			continue
		}
		n, err := s.conn.Read(data)
		if n > 0 {
			s.mu.Lock()
			s.buffer = append(s.buffer, data[:n]...)
			s.mu.Unlock()
			s.notify(s.ready)
		}
		if err != nil {
			if err != io.EOF {
				s.mu.Lock()
				if !s.closed {
					s.readFailed = true
				}
				s.mu.Unlock()
			}
			return
		}
	}
}

// Server connects only to one configured literal loopback backend. Requests
// cannot choose a destination, and unauthenticated peers cannot create sessions.
type Server struct {
	target   string
	token    [32]byte
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	sessions map[string]*session
	closed   bool
	wg       sync.WaitGroup
}

func NewServer(ctx context.Context, target, token string) (*Server, error) {
	host, port, err := net.SplitHostPort(target)
	ip := net.ParseIP(host)
	n, portErr := strconv.Atoi(port)
	if err != nil || ip == nil || !ip.IsLoopback() || portErr != nil || n < 1 || n > 65535 {
		return nil, errors.New("backend must be a literal loopback host:port")
	}
	if len(token) < 32 {
		return nil, errors.New("use a random token of at least 32 characters")
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Server{target: target, token: sha256.Sum256([]byte(token)), ctx: ctx, cancel: cancel, sessions: make(map[string]*session)}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				s.mu.Lock()
				for id, stream := range s.sessions {
					stream.mu.Lock()
					stale := now.Sub(stream.last) > 90*time.Second
					stream.mu.Unlock()
					if stale {
						delete(s.sessions, id)
						stream.close()
					}
				}
				s.mu.Unlock()
			}
		}
	}()
	return s, nil
}

func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	for id, stream := range s.sessions {
		delete(s.sessions, id)
		stream.close()
	}
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/sync" {
		http.NotFound(w, r)
		return
	}
	got := sha256.Sum256([]byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")))
	if subtle.ConstantTimeCompare(got[:], s.token[:]) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var q request
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if err := d.Decode(&q); err != nil {
		// A request cut in transit must be retried with the same offsets;
		// treating incomplete delivery as a permanent protocol error would
		// defeat the relay's purpose before any backend bytes were committed.
		http.Error(w, "incomplete request", http.StatusRequestTimeout)
		return
	}
	if len(q.ID) != 32 || len(q.Data) > chunkSize || q.Offset+uint64(len(q.Data)) < q.Offset {
		http.Error(w, "bad request", 400)
		return
	}
	if _, err := hex.DecodeString(q.ID); err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	if d.Decode(new(any)) != io.EOF {
		http.Error(w, "bad request", 400)
		return
	}
	s.mu.Lock()
	stream := s.sessions[q.ID]
	if s.closed {
		s.mu.Unlock()
		http.Error(w, "stopped", 503)
		return
	}
	if q.Close {
		if stream != nil {
			delete(s.sessions, q.ID)
			stream.close()
		}
		s.mu.Unlock()
		w.WriteHeader(204)
		return
	}
	if stream == nil && q.Open {
		if len(s.sessions) >= maxSessions {
			s.mu.Unlock()
			http.Error(w, "session limit", 503)
			return
		}
		conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(s.ctx, "tcp", s.target)
		if err != nil {
			s.mu.Unlock()
			http.Error(w, "backend unavailable", 502)
			return
		}
		stream = &session{conn: conn, last: time.Now(), ready: make(chan struct{}, 1), wake: make(chan struct{}, 1)}
		s.sessions[q.ID] = stream
		s.wg.Add(1)
		go func() { defer s.wg.Done(); stream.read(s.ctx) }()
	}
	s.mu.Unlock()
	if stream == nil {
		http.Error(w, "session expired", 410)
		return
	}
	stream.serial.Lock()
	defer stream.serial.Unlock()
	stream.mu.Lock()
	if stream.closed || q.Ack < stream.download || q.Ack > stream.offered || q.Offset > stream.upload || (q.Offset < stream.upload && q.Offset+uint64(len(q.Data)) > stream.upload) || (stream.sentEOF && len(q.Data) > 0 && q.Offset == stream.upload) {
		stream.mu.Unlock()
		http.Error(w, "invalid stream offset", 409)
		return
	}
	trim := int(q.Ack - stream.download)
	stream.buffer = stream.buffer[trim:]
	stream.download = q.Ack
	stream.last = time.Now()
	write := q.Offset == stream.upload && len(q.Data) > 0
	sendEOF := q.EOF && !stream.sentEOF
	stream.mu.Unlock()
	stream.notify(stream.wake)
	if write {
		stream.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		n, err := writeAll(stream.conn, q.Data)
		stream.mu.Lock()
		stream.upload += uint64(n)
		stream.mu.Unlock()
		if err != nil {
			stream.close()
			http.Error(w, "backend write failed", 502)
			return
		}
	}
	if sendEOF {
		if tcp, ok := stream.conn.(*net.TCPConn); ok {
			if err := tcp.CloseWrite(); err != nil {
				stream.close()
				http.Error(w, "backend EOF failed", 502)
				return
			}
		}
		stream.mu.Lock()
		stream.sentEOF = true
		stream.mu.Unlock()
	}
	// Let a fast backend reply within this same short request, never a
	// long-poll that keeps a filtered TCP connection alive indefinitely.
	stream.mu.Lock()
	empty := len(stream.buffer) == 0 && !stream.readEOF
	stream.mu.Unlock()
	if empty {
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-r.Context().Done():
		case <-stream.ready:
		case <-timer.C:
		}
		timer.Stop()
	}
	stream.mu.Lock()
	if stream.readFailed && len(stream.buffer) == 0 {
		stream.mu.Unlock()
		http.Error(w, "backend stream lost", http.StatusGone)
		return
	}
	n := min(chunkSize, len(stream.buffer))
	data := append([]byte(nil), stream.buffer[:n]...)
	p := response{Ack: stream.upload, Offset: stream.download, Data: data, EOF: stream.readEOF && !stream.readFailed && n == len(stream.buffer)}
	stream.offered = max(stream.offered, stream.download+uint64(n))
	stream.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "close")
	_ = json.NewEncoder(w).Encode(p)
}

func writeAll(w io.Writer, p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		n, err := w.Write(p)
		total += n
		p = p[n:]
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}

type Client struct {
	endpoint, token string
	http            *http.Client
}

type relayStatusError int

func (e relayStatusError) Error() string { return fmt.Sprintf("relay status %d", int(e)) }

func NewClient(endpoint, token, ca string) (*Client, error) {
	if !strings.HasPrefix(endpoint, "https://") || len(token) < 32 {
		return nil, errors.New("HTTPS endpoint and a random shared token are required")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("endpoint must be an HTTPS origin without credentials, path or query")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(ca)) {
		return nil, errors.New("invalid private CA")
	}
	// No environment proxy, no HTTP2 connection sharing. A new TLS connection
	// per request is the mechanism; small classical ECDHE handshakes keep the
	// request below the measured packet budget. Verification stays enabled.
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true, ForceAttemptHTTP2: false, DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext, TLSHandshakeTimeout: 3 * time.Second,
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256}, NextProtos: []string{"http/1.1"}, ClientSessionCache: tls.NewLRUClientSessionCache(64)}}
	return &Client{endpoint: strings.TrimRight(endpoint, "/") + "/sync", token: token, http: &http.Client{Transport: tr, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) exchange(ctx context.Context, q request) (response, error) {
	body, _ := json.Marshal(q)
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return response{}, err
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	r.Header.Set("Content-Type", "application/json")
	r.Close = true
	reply, err := c.http.Do(r)
	if err != nil {
		return response{}, err
	}
	defer reply.Body.Close()
	if reply.StatusCode != 200 {
		return response{}, relayStatusError(reply.StatusCode)
	}
	var p response
	d := json.NewDecoder(io.LimitReader(reply.Body, 4096))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, err
	}
	if len(p.Data) > chunkSize || d.Decode(new(any)) != io.EOF {
		return p, errors.New("invalid relay response")
	}
	return p, nil
}

// Forward owns one accepted local TCP connection. CloseWrite is transported as
// a logical EOF; delayed backend replies remain readable afterwards.
func (c *Client) Forward(parent context.Context, local net.Conn) (resultErr error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer local.Close()
	stop := context.AfterFunc(ctx, func() { local.Close() })
	defer stop()
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return err
	}
	q := request{ID: hex.EncodeToString(id), Open: true}
	attempted := false
	defer func() {
		if attempted {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(parent), time.Second)
			_, _ = c.exchange(cleanup, request{ID: q.ID, Close: true})
			cancel()
		}
	}()
	type readPart struct {
		data []byte
		err  error
	}
	parts := make(chan readPart, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			buf := make([]byte, chunkSize)
			n, err := local.Read(buf)
			select {
			case parts <- readPart{buf[:n], err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() {
		if resultErr != nil {
			if tcp, ok := local.(*net.TCPConn); ok {
				_ = tcp.SetLinger(0)
			}
		}
		cancel()
		local.Close()
		<-readerDone
	}()
	uploadEOF, downloadEOF := false, false
	lastSuccess := time.Now()
	for ctx.Err() == nil {
		if len(q.Data) == 0 && !uploadEOF {
			select {
			case part := <-parts:
				q.Data = part.data
				if part.err != nil {
					if part.err != io.EOF {
						return part.err
					}
					uploadEOF = true
				}
			default:
			}
		}
		q.EOF = uploadEOF
		attempted = true
		p, err := c.exchange(ctx, q)
		if err != nil {
			var status relayStatusError
			var verification *tls.CertificateVerificationError
			if errors.As(err, &verification) || (errors.As(err, &status) && (status == 400 || status == 401 || status == 409 || status == 410 || status == 413)) {
				return err
			}
			if time.Since(lastSuccess) > 15*time.Second {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			continue // retry the same offsets, no duplicate bytes
		}
		lastSuccess = time.Now()
		q.Open = false
		if p.Ack < q.Offset || p.Ack > q.Offset+uint64(len(q.Data)) || p.Offset != q.Ack || (downloadEOF && len(p.Data) > 0) {
			return errors.New("invalid acknowledged stream offsets")
		}
		consumed := int(p.Ack - q.Offset)
		q.Data = q.Data[consumed:]
		q.Offset = p.Ack
		if len(p.Data) > 0 {
			if _, err := writeAll(local, p.Data); err != nil {
				return err
			}
			q.Ack += uint64(len(p.Data))
		}
		if p.EOF && !downloadEOF {
			downloadEOF = true
			if tcp, ok := local.(*net.TCPConn); ok {
				if err := tcp.CloseWrite(); err != nil {
					return err
				}
			}
		}
		if uploadEOF && downloadEOF && len(q.Data) == 0 {
			return nil
		}
		if len(q.Data) == 0 && len(p.Data) == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	return ctx.Err()
}

func (c *Client) Serve(ctx context.Context, l net.Listener, onError func(error)) error {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() { cancel(); l.Close(); wg.Wait() }()
	slots := make(chan struct{}, maxSessions)
	stop := context.AfterFunc(ctx, func() { l.Close() })
	defer stop()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			if err := c.Forward(ctx, conn); err != nil && ctx.Err() == nil && onError != nil {
				onError(err)
			}
		}()
	}
}
