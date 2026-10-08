package naive

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/sirupsen/logrus"
)

// Helper owns exactly one child at a time, its private generated configuration,
// and bounded restart/shutdown. Readiness here means the helper accepts local
// connections; only traffic through the reverse engine proves tunnel health.
type Helper struct {
	cancel context.CancelFunc
	done   chan struct{}
	addr   string
	dir    string
	once   sync.Once
}

func (h *Helper) ProxyURL() string { return "socks5://" + h.addr }
func (h *Helper) Close()           { h.once.Do(h.cancel); <-h.done }

func StartClient(ctx context.Context, c *config.ClientConfig, log *logrus.Logger) (*Helper, error) {
	if err := ValidateClient(c); err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	addr := l.Addr().String()
	body, err := clientJSON(c.Naive, addr)
	if err != nil {
		l.Close()
		return nil, err
	}
	// Reserve the selected port while writing the configuration. The official
	// client cannot adopt a listener; release it immediately before spawning.
	defer l.Close()
	env := os.Environ()
	if c.Naive.CAFile != "" {
		env = replaceEnv(env, "SSL_CERT_FILE", c.Naive.CAFile)
	}
	return startManaged(ctx, c.Naive.Binary, "client", body, addr, env, log, l.Close)
}

func StartServer(ctx context.Context, c *config.ServerConfig, log *logrus.Logger) (*Helper, error) {
	if err := ValidateServer(c); err != nil {
		return nil, err
	}
	body, err := serverJSON(c)
	if err != nil {
		return nil, err
	}
	// Refuse a listener already held by another process. Otherwise readiness
	// could connect to that process before the helper's bind failure is reaped.
	l, err := net.Listen("tcp", c.Naive.Listen)
	if err != nil {
		return nil, fmt.Errorf("reserve Naive server listener: %w", err)
	}
	defer l.Close()
	return startManaged(ctx, c.Naive.Binary, "server", body, c.Naive.Listen, os.Environ(), log, l.Close)
}

func StartXrayClient(ctx context.Context, c *config.ClientConfig, log *logrus.Logger) (*Helper, error) {
	if err := ValidateXrayClient(c); err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer l.Close()
	addr := l.Addr().String()
	body, err := XrayClientJSON(c, addr)
	if err != nil {
		return nil, err
	}
	return startManaged(ctx, c.Xray.Binary, "xray-client", body, addr, os.Environ(), log, l.Close)
}

func StartXrayServer(ctx context.Context, c *config.ServerConfig, log *logrus.Logger) (*Helper, error) {
	if err := ValidateXrayServer(c); err != nil {
		return nil, err
	}
	body, err := XrayServerJSON(c)
	if err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp", c.Xray.Listen)
	if err != nil {
		return nil, fmt.Errorf("reserve Xray server listener: %w", err)
	}
	defer l.Close()
	return startManaged(ctx, c.Xray.Binary, "xray-server", body, c.Xray.Listen, os.Environ(), log, l.Close)
}

func helperLabel(mode string) string {
	if strings.HasPrefix(mode, "xray-") {
		return "Xray"
	}
	return "Naive"
}

func replaceEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, v := range env {
		if len(v) < len(prefix) || v[:len(prefix)] != prefix {
			out = append(out, v)
		}
	}
	return append(out, prefix+value)
}

func startManaged(parent context.Context, binary, mode string, body []byte, addr string, env []string, log *logrus.Logger, release func() error) (*Helper, error) {
	if parent.Err() != nil {
		return nil, parent.Err()
	}
	label := helperLabel(mode)
	if log == nil {
		log = logrus.StandardLogger()
	}
	dir, err := os.MkdirTemp("", "backpack-"+strings.ToLower(label)+"-")
	if err != nil {
		return nil, err
	}
	file := filepath.Join(dir, "helper.json")
	if err = os.WriteFile(file, body, 0o600); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	h := &Helper{cancel: cancel, done: make(chan struct{}), addr: addr, dir: dir}
	if mode == "server" || strings.HasPrefix(mode, "xray-") {
		checkCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		args := []string{"check", "-c", file}
		if label == "Xray" {
			args = []string{"run", "-test", "-c", file}
		}
		cmd := exec.CommandContext(checkCtx, binary, args...)
		configureProcess(cmd)
		cmd.Cancel = func() error { return killProcess(cmd.Process) }
		cmd.Env = env
		// Use actual file descriptors so failures remain diagnosable without
		// copy pipes that descendants could retain after the leader exits.
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		err = cmd.Run()
		if cmd.Process != nil {
			_ = killProcess(cmd.Process)
		}
		stop()
		if err != nil {
			cancel()
			os.RemoveAll(dir)
			return nil, fmt.Errorf("%s %s configuration check failed: %w", label, mode, err)
		}
	}
	if release != nil {
		if err = release(); err != nil {
			cancel()
			os.RemoveAll(dir)
			return nil, err
		}
	}
	ready := make(chan error, 1)
	go func() {
		defer close(h.done)
		defer os.RemoveAll(dir)
		first := true
		for ctx.Err() == nil {
			args := []string{file}
			if mode == "server" || label == "Xray" {
				args = []string{"run", "-c", file}
			}
			cmd := exec.Command(binary, args...)
			configureProcess(cmd)
			cmd.Env = env
			cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
			if err := cmd.Start(); err != nil {
				if first {
					ready <- fmt.Errorf("starting %s %s helper: %w", label, mode, err)
					return
				}
				log.Warnf("%s %s helper could not restart; retrying", label, mode)
				if !sleep(ctx, time.Second) {
					return
				}
				continue
			}
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait(); close(exited) }()
			if first {
				err := waitReady(ctx, addr, mode, exited)
				if err != nil {
					stopChild(cmd, exited)
					ready <- err
					return
				}
				first = false
				ready <- nil
			}
			log.Infof("%s %s helper started (PID %d)", label, mode, cmd.Process.Pid)
			select {
			case <-ctx.Done():
				stopChild(cmd, exited)
				return
			case <-exited:
				// Also remove descendants after the leader has exited.
				_ = killProcess(cmd.Process)
				if ctx.Err() != nil {
					return
				}
				log.Warnf("%s %s helper exited; restarting in one second", label, mode)
			}
			if !sleep(ctx, time.Second) {
				return
			}
		}
		if first {
			ready <- ctx.Err()
		}
	}()
	select {
	case err := <-ready:
		if err != nil {
			h.Close()
			return nil, err
		}
		return h, nil
	case <-ctx.Done():
		h.Close()
		return nil, ctx.Err()
	}
}

func stopChild(cmd *exec.Cmd, exited <-chan error) {
	_ = terminateProcess(cmd.Process)
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		_ = killProcess(cmd.Process)
		<-exited
	}
	_ = killProcess(cmd.Process)
}

func waitReady(ctx context.Context, addr, mode string, exited <-chan error) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	addr = net.JoinHostPort(host, port)
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		select {
		case err := <-exited:
			// Put no helper output or credentials in the tunnel log.
			return fmt.Errorf("%s %s helper exited before readiness: %v", helperLabel(mode), mode, err)
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.SetDeadline(time.Now().Add(200 * time.Millisecond))
			if mode == "client" || mode == "xray-client" {
				_, err = conn.Write([]byte{5, 1, 0})
				if err == nil {
					var reply [2]byte
					_, err = io.ReadFull(conn, reply[:])
					if err == nil && reply != [2]byte{5, 0} {
						err = errors.New("unexpected SOCKS reply")
					}
				}
			}
			conn.Close()
			if err == nil {
				return nil
			}
		}
		if !sleep(ctx, 50*time.Millisecond) {
			return ctx.Err()
		}
	}
	return fmt.Errorf("%s %s helper did not become ready within 10 seconds", helperLabel(mode), mode)
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// DuplexConn carries directional EOF inside the helper's byte stream. Official
// proxy helpers may close both directions on a physical FIN; keeping the carrier
// open until both relays finish preserves a backend's reply after request EOF.
// Both managed peers must use this framing. Ordinary TCP remains unframed.
type DuplexConn struct {
	net.Conn
	readMu, writeMu         sync.Mutex
	readHeader, writeHeader [4]byte
	headerRead              int
	remaining               uint32
	readEOF, writeEOF       bool
	writeErr                error
	writeParts              [2][]byte
	writeBuffers            net.Buffers
}

func WrapDuplex(conn net.Conn) *DuplexConn {
	c := &DuplexConn{Conn: conn}
	c.writeBuffers = c.writeParts[:]
	return c
}

// PreservesDirectionalEOF identifies the explicit framing capability to the
// relay without exposing the raw connection to zero-copy bypasses.
func (*DuplexConn) PreservesDirectionalEOF() {}

func (c *DuplexConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if c.readEOF {
		return 0, io.EOF
	}
	for c.remaining == 0 {
		for c.headerRead < len(c.readHeader) {
			n, err := c.Conn.Read(c.readHeader[c.headerRead:])
			c.headerRead += n
			if err != nil {
				if errors.Is(err, io.EOF) && c.headerRead == len(c.readHeader) && binary.BigEndian.Uint32(c.readHeader[:]) == 0 {
					c.readEOF = true
					return 0, io.EOF
				}
				return 0, c.readFailure(err)
			}
			if n == 0 {
				return 0, nil
			}
		}
		c.remaining = binary.BigEndian.Uint32(c.readHeader[:])
		c.headerRead = 0
		if c.remaining == 0 {
			c.readEOF = true
			return 0, io.EOF
		}
		if c.remaining > 65536 {
			c.Conn.Close()
			return 0, errors.New("managed helper frame exceeds 65536 bytes")
		}
	}
	if len(p) > int(c.remaining) {
		p = p[:c.remaining]
	}
	n, err := c.Conn.Read(p)
	c.remaining -= uint32(n)
	if err != nil {
		err = c.readFailure(err)
	}
	return n, err
}

func (c *DuplexConn) readFailure(err error) error {
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		c.Conn.Close()
	}
	return err
}

func (c *DuplexConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	if c.writeEOF {
		return 0, io.ErrClosedPipe
	}
	written := 0
	for len(p) != 0 {
		size := min(len(p), 65536)
		binary.BigEndian.PutUint32(c.writeHeader[:], uint32(size))
		if _, ok := c.Conn.(*net.TCPConn); ok {
			// One vectored TCP write carries both header and data, avoiding an
			// extra syscall/small packet for each record. Never bypass framing.
			c.writeParts[0], c.writeParts[1] = c.writeHeader[:], p[:size]
			c.writeBuffers = c.writeParts[:]
			n, err := c.writeBuffers.WriteTo(c.Conn)
			clear(c.writeParts[:])
			written += max(0, int(n)-len(c.writeHeader))
			if err == nil && n != int64(len(c.writeHeader)+size) {
				err = io.ErrShortWrite
			}
			if err != nil {
				c.writeErr = err
				c.Conn.Close()
				return written, err
			}
			p = p[size:]
			continue
		}
		if _, err := c.writeRecordPart(c.writeHeader[:]); err != nil {
			return written, err
		}
		n, err := c.writeRecordPart(p[:size])
		written += n
		if err != nil {
			return written, err
		}
		p = p[size:]
	}
	return written, nil
}

func (c *DuplexConn) writeRecordPart(p []byte) (int, error) {
	written := 0
	for len(p) != 0 {
		n, err := c.Conn.Write(p)
		written += n
		p = p[n:]
		if err == nil && n == 0 {
			err = io.ErrShortWrite
		}
		if err != nil {
			c.writeErr = err
			c.Conn.Close() // A partial record cannot safely be retried.
			return written, err
		}
	}
	return written, nil
}

func (c *DuplexConn) CloseWrite() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.writeErr != nil {
		return c.writeErr
	}
	if c.writeEOF {
		return nil
	}
	c.writeEOF = true
	clear(c.writeHeader[:])
	_, err := c.writeRecordPart(c.writeHeader[:])
	return err
}
