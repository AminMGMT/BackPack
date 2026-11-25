package core

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/utils/network"
)

// Scrap (Backpack): a tunnel connection dressed as plain HTTP.
//
// The kharej opens every connection with an HTTP/1.1 POST of the kind a
// browser or an app's uploader sends — Chrome's headers, a chunked body — and
// the Iran server answers 200 from "nginx" with a chunked body of its own.
// The two bodies are the tunnel: stealth+ inside the chunks, so a body is
// random bytes with no framing of its own showing.
//
// The request proves the token in a cookie: a random nonce and an HMAC over
// it and the minute, accepted within a few minutes and once. A request
// without it gets what nginx gives a path that is not there — a 404 page —
// and anything that is not HTTP gets nginx's 400; neither says a tunnel is
// here. The handshake rides with the request, so a connection costs the one
// round trip stealth's does.

const (
	scrapHeadMax  = 8 << 10
	scrapSkew     = 3 // minutes either way a cookie is accepted
	scrapNonceLen = 16
	scrapMACLen   = 16
	scrapCookie   = "sid"
	// scrapChunkMax is the most one chunk carries.
	scrapChunkMax = 32 << 10
)

// scrapPaths are what the POST goes to; one is drawn per connection.
var scrapPaths = []string{"/upload", "/api/v1/sync", "/api/upload", "/v2/stream", "/collect", "/api/v1/events"}

const scrapUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

func scrapMAC(token string, nonce []byte, minute int64) []byte {
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte("backpack-scrap-v1"))
	m.Write(nonce)
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(minute))
	m.Write(b[:])
	return m.Sum(nil)[:scrapMACLen]
}

// scrapRequest is the request head the kharej sends.
func scrapRequest(token, host string) ([]byte, error) {
	nonce := make([]byte, scrapNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	cookie := base64.RawURLEncoding.EncodeToString(append(nonce, scrapMAC(token, nonce, time.Now().Unix()/60)...))
	if host == "" {
		host = "localhost"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "POST %s HTTP/1.1\r\n", scrapPaths[mrand.IntN(len(scrapPaths))])
	fmt.Fprintf(&b, "Host: %s\r\n", host)
	b.WriteString("Connection: keep-alive\r\n")
	b.WriteString("User-Agent: " + scrapUA + "\r\n")
	b.WriteString("Accept: */*\r\n")
	b.WriteString("Accept-Encoding: gzip, deflate\r\n")
	b.WriteString("Accept-Language: en-US,en;q=0.9\r\n")
	b.WriteString("Content-Type: application/octet-stream\r\n")
	fmt.Fprintf(&b, "Cookie: %s=%s\r\n", scrapCookie, cookie)
	b.WriteString("Transfer-Encoding: chunked\r\n\r\n")
	return []byte(b.String()), nil
}

// scrapSeen remembers accepted nonces until they could no longer be valid,
// so a captured request cannot be played again.
var scrapSeen = struct {
	sync.Mutex
	m    map[string]time.Time
	last time.Time
}{m: map[string]time.Time{}}

func scrapFresh(nonce []byte) bool {
	now := time.Now()
	scrapSeen.Lock()
	defer scrapSeen.Unlock()
	if now.Sub(scrapSeen.last) > time.Minute {
		for k, until := range scrapSeen.m {
			if now.After(until) {
				delete(scrapSeen.m, k)
			}
		}
		scrapSeen.last = now
	}
	k := string(nonce)
	if _, dup := scrapSeen.m[k]; dup {
		return false
	}
	scrapSeen.m[k] = now.Add((2*scrapSkew + 1) * time.Minute)
	return true
}

// scrapAuthorised reports whether a request carries this tunnel's cookie.
func scrapAuthorised(req *http.Request, token string) bool {
	if req.Method != http.MethodPost {
		return false
	}
	c, err := req.Cookie(scrapCookie)
	if err != nil {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || len(raw) != scrapNonceLen+scrapMACLen {
		return false
	}
	nonce, mac := raw[:scrapNonceLen], raw[scrapNonceLen:]
	now := time.Now().Unix() / 60
	for d := int64(-scrapSkew); d <= scrapSkew; d++ {
		if hmac.Equal(mac, scrapMAC(token, nonce, now+d)) {
			return scrapFresh(nonce)
		}
	}
	return false
}

const scrapOK = "HTTP/1.1 200 OK\r\nServer: nginx\r\nDate: %s\r\nContent-Type: application/octet-stream\r\n" +
	"Transfer-Encoding: chunked\r\nConnection: keep-alive\r\nCache-Control: no-store\r\n\r\n"

// nginx's own pages, byte for byte as its stock build serves them.
const scrapNotFoundBody = "<html>\r\n<head><title>404 Not Found</title></head>\r\n<body>\r\n<center><h1>404 Not Found</h1></center>\r\n<hr><center>nginx</center>\r\n</body>\r\n</html>\r\n"
const scrapBadRequestBody = "<html>\r\n<head><title>400 Bad Request</title></head>\r\n<body>\r\n<center><h1>400 Bad Request</h1></center>\r\n<hr><center>nginx</center>\r\n</body>\r\n</html>\r\n"

func scrapPage(status, body string, keep bool) []byte {
	conn := "close"
	if keep {
		conn = "keep-alive"
	}
	return []byte(fmt.Sprintf("HTTP/1.1 %s\r\nServer: nginx\r\nDate: %s\r\nContent-Type: text/html\r\nContent-Length: %d\r\nConnection: %s\r\n\r\n%s",
		status, time.Now().UTC().Format(http.TimeFormat), len(body), conn, body))
}

var errScrapRefused = errors.New("scrap: not this tunnel's request")

// scrapClient opens c as the kharej: request, the stealth+ handshake inside
// the body, and the server's 200.
func scrapClient(c net.Conn, token, host string) (net.Conn, error) {
	head, err := scrapRequest(token, host)
	if err != nil {
		return nil, err
	}
	_ = c.SetDeadline(time.Now().Add(layerHandshakeTimeout))
	if _, err := c.Write(head); err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(c, 4096)
	ch := &chunkConn{Conn: c, r: br, awaitHead: true}
	w, err := network.StealthPlusClientConn(ch, token, layerHandshakeTimeout)
	if err != nil {
		return nil, err
	}
	_ = c.SetDeadline(time.Time{})
	return w, nil
}

// scrapServer opens c as the Iran server: a request with the cookie gets 200
// and the tunnel; anything else gets nginx's answer and is closed.
func scrapServer(c net.Conn, token string) (net.Conn, error) {
	_ = c.SetDeadline(time.Now().Add(layerHandshakeTimeout))
	br := bufio.NewReaderSize(c, scrapHeadMax)
	req, err := http.ReadRequest(br)
	if err != nil {
		// nginx closes a client that never finishes its request without a
		// word, and answers one that sent something else with 400.
		var ne net.Error
		if !errors.As(err, &ne) || !ne.Timeout() {
			_, _ = c.Write(scrapPage("400 Bad Request", scrapBadRequestBody, false))
		}
		return nil, errScrapRefused
	}
	if !scrapAuthorised(req, token) {
		_, _ = c.Write(scrapPage("404 Not Found", scrapNotFoundBody, false))
		return nil, errScrapRefused
	}
	if _, err := c.Write([]byte(fmt.Sprintf(scrapOK, time.Now().UTC().Format(http.TimeFormat)))); err != nil {
		return nil, err
	}
	ch := &chunkConn{Conn: c, r: br}
	w, err := network.StealthPlusServerConn(ch, token, layerHandshakeTimeout)
	if err != nil {
		return nil, err
	}
	_ = c.SetDeadline(time.Time{})
	return w, nil
}

// chunkConn is an HTTP/1.1 chunked body each way: every Write one chunk,
// Read the chunks' data in order. On the kharej it first reads past the
// server's response head (awaitHead), which arrives before the first chunk.
type chunkConn struct {
	net.Conn
	r         *bufio.Reader
	awaitHead bool

	wmu sync.Mutex
	wb  []byte

	rmu  sync.Mutex
	left int
}

// NetConn is the socket under the chunks.
func (c *chunkConn) NetConn() net.Conn { return c.Conn }

func (c *chunkConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	total := 0
	for len(p) > 0 {
		n := min(len(p), scrapChunkMax)
		c.wb = strconv.AppendInt(c.wb[:0], int64(n), 16)
		c.wb = append(c.wb, '\r', '\n')
		c.wb = append(c.wb, p[:n]...)
		c.wb = append(c.wb, '\r', '\n')
		if _, err := c.Conn.Write(c.wb); err != nil {
			return total, err
		}
		total += n
		p = p[n:]
	}
	return total, nil
}

func (c *chunkConn) Read(p []byte) (int, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	if c.awaitHead {
		resp, err := http.ReadResponse(c.r, nil)
		if err != nil {
			return 0, err
		}
		if resp.StatusCode != http.StatusOK {
			return 0, fmt.Errorf("scrap: the server answered %s — not this tunnel, or another token", resp.Status)
		}
		c.awaitHead = false
	}
	for c.left == 0 {
		line, err := c.r.ReadSlice('\n')
		if err != nil {
			return 0, err
		}
		size := strings.TrimSpace(string(line))
		if i := strings.IndexByte(size, ';'); i >= 0 {
			size = size[:i]
		}
		n, err := strconv.ParseInt(size, 16, 32)
		if err != nil || n < 0 || n > scrapChunkMax {
			return 0, fmt.Errorf("scrap: bad chunk header %q", size)
		}
		if n == 0 {
			return 0, io.EOF
		}
		c.left = int(n)
	}
	if len(p) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= n
	if c.left == 0 && err == nil {
		var crlf [2]byte
		if _, err = io.ReadFull(c.r, crlf[:]); err == nil && (crlf[0] != '\r' || crlf[1] != '\n') {
			err = fmt.Errorf("scrap: chunk not terminated")
		}
	}
	return n, err
}
