package webui

// A root shell on this machine, in the browser.
//
// The panel is already root here — it writes systemd units and tunnel configs,
// installs updates, restores backups — so a terminal adds no power the
// password did not already hold. What it adds is a door that is easy to leave
// open, and that is what the rules below are for:
//
//   - A browser session only. requireAdmin lets an admin-scoped token through;
//     this refuses every token outright. A token is for a scraper or a script,
//     and neither has any business with an interactive root shell.
//   - The same origin only. A WebSocket is not covered by the browser's
//     same-origin rules the way a fetch is, so without the Origin check any page
//     the operator visited while signed in could open one. gorilla's default
//     CheckOrigin refuses a cross-origin upgrade; it is kept.
//   - Said out loud. Opening one is written to the audit record and to the
//     alert feed — which the monitor forwards to Telegram — so a shell nobody
//     meant to open is a message on the operator's phone, not a secret.
//   - Bounded. A handful at once, and the whole process tree ends with the page.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"github.com/backpack/backpack/internal/alerthist"
)

// terminalMax is how many shells may be open at once, across every browser.
const terminalMax = 4

// terminalCount is the number of shells open now.
type terminalCount struct {
	mu sync.Mutex
	n  int
}

func (c *terminalCount) take() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n >= terminalMax {
		return false
	}
	c.n++
	return true
}

func (c *terminalCount) give() {
	c.mu.Lock()
	c.n--
	c.mu.Unlock()
}

var termUpgrader = websocket.Upgrader{
	ReadBufferSize:  16 << 10,
	WriteBufferSize: 32 << 10,
	// CheckOrigin is left nil on purpose: gorilla then refuses an upgrade whose
	// Origin is not this host. See the note at the top of this file.
}

// termControl is a message the page sends that is not keystrokes.
type termControl struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// termShell is the shell to run: root's login shell when it can be found,
// bash, then sh.
func termShell() (string, []string) {
	for _, sh := range []string{"/bin/bash", "/usr/bin/bash", "/bin/zsh", "/bin/sh"} {
		if _, err := os.Stat(sh); err == nil {
			if sh == "/bin/sh" {
				return sh, []string{"-l"}
			}
			return sh, []string{"--login"}
		}
	}
	return "/bin/sh", nil
}

func dim(q string, def uint16) uint16 {
	n, err := strconv.Atoi(q)
	if err != nil || n <= 0 || n > 1000 {
		return def
	}
	return uint16(n)
}

func (s *server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	// A browser session and nothing else.
	c, err := r.Cookie(sessionCookie)
	if bearer(r) != "" || err != nil {
		http.Error(w, "the terminal is only for a signed-in browser, never a token", http.StatusForbidden)
		return
	}
	sessionDone := s.sessions.watch(c.Value)
	if sessionDone == nil {
		http.Error(w, "the terminal session has ended", http.StatusForbidden)
		return
	}
	if !websocket.IsWebSocketUpgrade(r) {
		http.Error(w, "the terminal is a WebSocket", http.StatusBadRequest)
		return
	}
	if !s.terminals.take() {
		http.Error(w, fmt.Sprintf("%d terminals are already open — close one first", terminalMax), http.StatusTooManyRequests)
		return
	}
	defer s.terminals.give()

	ws, err := termUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return // the upgrader has already answered
	}
	defer ws.Close()
	select {
	case <-sessionDone:
		return
	default:
	}
	// The server's read and write timeouts were set on this connection for an
	// ordinary request and would end a shell after thirty seconds.
	_ = ws.NetConn().SetDeadline(time.Time{})

	ip := clientIP(r)
	record(auditEntry{At: time.Now().Unix(), Who: "session " + sessionID(c.Value), IP: ip,
		Method: r.Method, Path: r.URL.Path, Action: "open", Status: http.StatusSwitchingProtocols})
	alerthist.RecordEvent(fmt.Sprintf("🖥 A root terminal was opened in the web panel from %s", ip))

	shell, args := termShell()
	cmd := exec.Command(shell, args...)
	home := os.Getenv("HOME")
	if home == "" {
		home = "/root"
	}
	cmd.Dir = home
	cmd.Env = []string{
		"TERM=xterm-256color", "COLORTERM=truecolor", "LANG=C.UTF-8", "LC_ALL=C.UTF-8",
		"HOME=" + home, "SHELL=" + shell, "USER=root", "LOGNAME=root",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"BACKPACK_PANEL_TERMINAL=1",
	}
	q := r.URL.Query()
	pty, err := startPTY(cmd, dim(q.Get("rows"), 24), dim(q.Get("cols"), 80))
	if err != nil {
		_ = ws.WriteMessage(websocket.TextMessage, mustJSON(map[string]any{"type": "error", "message": err.Error()}))
		return
	}
	defer pty.Close()

	var wmu sync.Mutex
	write := func(kind int, b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		_ = ws.SetWriteDeadline(time.Now().Add(15 * time.Second))
		return ws.WriteMessage(kind, b)
	}

	// The shell's output, to the page.
	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		buf := make([]byte, 32<<10)
		for {
			n, err := pty.Read(buf)
			if n > 0 {
				if write(websocket.BinaryMessage, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// A ping now and then, so an idle shell behind a proxy is not cut for
	// being quiet, and a page that vanished without closing is noticed.
	stopPing := make(chan struct{})
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stopPing:
				return
			case <-t.C:
				wmu.Lock()
				err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
				wmu.Unlock()
				if err != nil {
					return
				}
			}
		}
	}()

	// Keystrokes and resizes, from the page.
	inDone := make(chan struct{})
	go func() {
		defer close(inDone)
		ws.SetReadLimit(1 << 20)
		for {
			kind, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if !s.sessions.valid(c.Value) {
				return
			}
			switch kind {
			case websocket.BinaryMessage:
				if _, err := pty.Write(data); err != nil {
					return
				}
			case websocket.TextMessage:
				var m termControl
				if json.Unmarshal(data, &m) == nil && m.Type == "resize" {
					_ = resizePTY(pty, m.Rows, m.Cols)
				}
			}
		}
	}()

	exited := make(chan int, 1)
	go func() {
		err := cmd.Wait()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		exited <- code
	}()

	select {
	case code := <-exited:
		// Let the last of the output reach the page before saying so.
		select {
		case <-outDone:
		case <-time.After(time.Second):
		}
		_ = write(websocket.TextMessage, mustJSON(map[string]any{"type": "exit", "code": code}))
	case <-inDone:
		// The page went away: hang up on the whole session, as closing a
		// terminal window does, and make sure of it a moment later.
		hangUp(cmd)
		<-exited
	case <-sessionDone:
		_ = ws.Close()
		_ = pty.Close()
		hangUp(cmd)
		<-exited
	}
	close(stopPing)
	_ = pty.Close()
	wmu.Lock()
	_ = ws.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	wmu.Unlock()
}

// hangUp ends a shell and everything it started.
func hangUp(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid // Setsid: the shell leads its own session and group
	_ = syscall.Kill(-pgid, syscall.SIGHUP)
	go func() {
		time.Sleep(3 * time.Second)
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}()
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
