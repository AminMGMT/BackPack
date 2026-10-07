package naive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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
	dir, err := os.MkdirTemp("", "backpack-naive-")
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
	if mode == "server" {
		checkCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		cmd := exec.CommandContext(checkCtx, binary, "check", "-c", file)
		configureProcess(cmd)
		cmd.Cancel = func() error { return killProcess(cmd.Process) }
		cmd.Env = env
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		err = cmd.Run()
		stop()
		if err != nil {
			cancel()
			os.RemoveAll(dir)
			return nil, fmt.Errorf("Naive server configuration check failed: %w", err)
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
			if mode == "server" {
				args = []string{"run", "-c", file}
			}
			cmd := exec.Command(binary, args...)
			configureProcess(cmd)
			cmd.Env = env
			cmd.Stdout = io.Discard
			cmd.Stderr = io.Discard
			if err := cmd.Start(); err != nil {
				if first {
					ready <- fmt.Errorf("starting Naive %s helper: %w", mode, err)
					return
				}
				log.Warnf("Naive %s helper could not restart; retrying", mode)
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
			log.Infof("Naive %s helper started (PID %d)", mode, cmd.Process.Pid)
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
				log.Warnf("Naive %s helper exited; restarting in one second", mode)
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
			return fmt.Errorf("Naive %s helper exited before readiness: %v", mode, err)
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.SetDeadline(time.Now().Add(200 * time.Millisecond))
			if mode == "client" {
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
	return fmt.Errorf("Naive %s helper did not become ready within 10 seconds", mode)
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
