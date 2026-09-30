package webui

// The Manage section: three of the menu's Manage tools, from the browser.
//
//   Auto Refresh        — Manage → Auto Refresh (restart every tunnel every N hours)
//   Built-in Proxy      — Manage → Built-in Proxy (SOCKS5/HTTP on 127.0.0.1)
//   File Locations      — Manage → File Locations
//
// Each calls what the menu calls; nothing here has its own idea of how the
// thing works. What is added is what a page can do that a terminal screen
// could not: a proxy is tested where it is configured, and every path says how big it is and when it last changed.

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/localproxy"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/schedule"
)

// --- the section's state, in one read ----------------------------------------

type manageProxy struct {
	Enabled     bool   `json:"enabled"`
	Running     bool   `json:"running"`
	Type        string `json:"type"`
	Port        int    `json:"port,omitempty"`
	Username    string `json:"username,omitempty"`
	HasPassword bool   `json:"hasPassword"`
}

type manageFile struct {
	Label    string `json:"label"`
	Path     string `json:"path"`
	Group    string `json:"group"`
	Exists   bool   `json:"exists"`
	Dir      bool   `json:"dir,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Items    int    `json:"items,omitempty"` // a folder: what is in it
	Modified int64  `json:"modified,omitempty"`
}

func proxyView() manageProxy {
	c := localproxy.Load()
	return manageProxy{
		Enabled: c.Enabled, Running: c.Enabled && manage.ProxyRunning(),
		Type: string(c.Type), Port: c.Port, Username: c.Username, HasPassword: c.Password != "",
	}
}

// fileGroup sorts a location into the part of the product it belongs to.
func fileGroup(label string) string {
	switch {
	case strings.HasPrefix(label, "Tunnel"):
		return "Tunnels"
	case strings.Contains(label, "service"):
		return "Services"
	case strings.Contains(label, "Backup") || strings.Contains(label, "Snapshot"):
		return "Backups"
	default:
		return "Backpack"
	}
}

func filesView() []manageFile {
	var out []manageFile
	for _, l := range manage.Locations() {
		f := manageFile{Label: l.Label, Path: l.Path, Group: fileGroup(l.Label), Exists: l.Exists}
		if st, err := os.Stat(l.Path); err == nil {
			f.Exists, f.Modified = true, st.ModTime().Unix()
			if st.IsDir() {
				f.Dir = true
				if ents, err := os.ReadDir(l.Path); err == nil {
					f.Items = len(ents)
				}
			} else {
				f.Size = st.Size()
			}
		}
		out = append(out, f)
	}
	return out
}

func (s *server) handleManage(w http.ResponseWriter, r *http.Request) {
	hours := schedule.AutoRefreshHours()
	writeJSON(w, map[string]any{
		"refresh": refreshView(hours),
		"proxy":   proxyView(),
		"files":   filesView(),
		"root":    os.Geteuid() == 0,
		"linux":   runtime.GOOS == "linux",
		"publicIP": func() string {
			if ip := manage.PublicIPv4(); ip != "-" {
				return ip
			}
			return ""
		}(),
	})
}

// --- Auto Refresh -----------------------------------------------------------

func (s *server) handleAutoRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	hours, err := strconv.Atoi(strings.TrimSpace(r.FormValue("hours")))
	if err != nil || hours < 0 || hours > 24*30 {
		http.Error(w, "hours must be a whole number from 0 (off) to 720", http.StatusBadRequest)
		return
	}
	if err := schedule.SetAutoRefresh(hours); err != nil {
		http.Error(w, "could not set the schedule: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// What cron will actually do, which is not always what was typed: above a
	// day it is whole days. The page says the number that is in force.
	writeJSON(w, refreshView(schedule.AutoRefreshHours()))
}

// refreshView is the schedule as the page draws it: what was asked, what cron
// does with it, and when it next fires on this server's clock — the page
// counts down to that, and the browser's clock and zone are not the server's.
func refreshView(hours int) map[string]any {
	v := map[string]any{"hours": hours, "effective": schedule.EffectiveHours(hours), "now": time.Now().Unix()}
	if next := schedule.NextRun(hours, time.Now()); !next.IsZero() {
		v["next"] = next.Unix()
		name, off := next.Zone()
		v["zone"], v["offset"] = name, off
	}
	return v
}

// --- Built-in Proxy ---------------------------------------------------------

func (s *server) handleProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, proxyView())
		return
	}
	_ = r.ParseForm()
	switch r.FormValue("action") {
	case "enable":
		c := localproxy.Load()
		switch localproxy.Kind(strings.ToLower(r.FormValue("type"))) {
		case localproxy.SOCKS5:
			c.Type = localproxy.SOCKS5
		case localproxy.HTTP:
			c.Type = localproxy.HTTP
		default:
			http.Error(w, "type must be socks5 or http", http.StatusBadRequest)
			return
		}
		port, err := strconv.Atoi(strings.TrimSpace(r.FormValue("port")))
		if err != nil || port < 1 || port > 65535 {
			http.Error(w, "the port must be 1–65535", http.StatusBadRequest)
			return
		}
		// A port something else holds would leave the service failing in a
		// restart loop; the one the proxy already has is its own to keep.
		if (port != c.Port || !manage.ProxyRunning()) && manage.PortInUse(strconv.Itoa(port)) {
			http.Error(w, fmt.Sprintf("port %d is already in use on this server", port), http.StatusBadRequest)
			return
		}
		c.Port = port
		c.Username = strings.TrimSpace(r.FormValue("username"))
		// An empty password field keeps the one already set, so the form can
		// be saved without retyping a secret the page never received.
		if pw := r.FormValue("password"); pw != "" || c.Username == "" {
			c.Password = pw
		}
		if (c.Username == "") != (c.Password == "") {
			http.Error(w, "give both a username and a password, or neither", http.StatusBadRequest)
			return
		}
		if err := manage.EnableProxyService(c); err != nil {
			http.Error(w, "could not enable the proxy: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, proxyView())
	case "disable":
		if err := manage.DisableProxyService(); err != nil {
			http.Error(w, "could not disable the proxy: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, proxyView())
	case "test":
		writeJSON(w, testProxy(localproxy.Load()))
	default:
		http.Error(w, "action must be enable, disable or test", http.StatusBadRequest)
	}
}

// proxyTest is what trying the proxy found.
type proxyTest struct {
	OK     bool   `json:"ok"`
	Stage  string `json:"stage"` // listening, handshake, auth
	Detail string `json:"detail"`
	Ms     int64  `json:"ms"`
}

// testProxy speaks to the proxy as a client would, on loopback: it connects,
// offers the handshake of the configured type with the configured
// credentials, and says how far it got. It asks for no destination, so it
// reaches nothing beyond the proxy itself.
func testProxy(c localproxy.Config) proxyTest {
	if !c.Enabled || c.Port <= 0 {
		return proxyTest{Stage: "listening", Detail: "the proxy is off"}
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", c.Addr(), 3*time.Second)
	if err != nil {
		return proxyTest{Stage: "listening", Detail: "nothing is listening on " + c.Addr() + ": " + err.Error()}
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	ms := func() int64 { return time.Since(start).Milliseconds() }
	if c.Type == localproxy.HTTP {
		req := "CONNECT 0.0.0.0:0 HTTP/1.1\r\nHost: 0.0.0.0:0\r\n"
		if c.Username != "" {
			req += "Proxy-Authorization: Basic " +
				base64.StdEncoding.EncodeToString([]byte(c.Username+":"+c.Password)) + "\r\n"
		}
		if _, err := conn.Write([]byte(req + "\r\n")); err != nil {
			return proxyTest{Stage: "handshake", Detail: err.Error(), Ms: ms()}
		}
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil || !strings.HasPrefix(line, "HTTP/") {
			return proxyTest{Stage: "handshake", Detail: "it did not answer as an HTTP proxy", Ms: ms()}
		}
		status := strings.TrimSpace(line)
		if strings.Contains(status, " 407") {
			return proxyTest{Stage: "auth", Detail: "it refused the configured credentials (" + status + ")", Ms: ms()}
		}
		return proxyTest{OK: true, Stage: "auth", Ms: ms(),
			Detail: "answers as an HTTP proxy" + map[bool]string{true: " and accepts the credentials", false: ""}[c.Username != ""]}
	}
	// SOCKS5: the greeting, and the username/password step when one is set.
	method := byte(0x00)
	if c.Username != "" {
		method = 0x02
	}
	if _, err := conn.Write([]byte{0x05, 0x01, method}); err != nil {
		return proxyTest{Stage: "handshake", Detail: err.Error(), Ms: ms()}
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[0] != 0x05 {
		return proxyTest{Stage: "handshake", Detail: "it did not answer as a SOCKS5 proxy", Ms: ms()}
	}
	if reply[1] != method {
		return proxyTest{Stage: "handshake", Ms: ms(),
			Detail: fmt.Sprintf("it wants authentication method %d and was offered %d", reply[1], method)}
	}
	if method == 0x02 {
		msg := []byte{0x01, byte(len(c.Username))}
		msg = append(msg, c.Username...)
		msg = append(msg, byte(len(c.Password)))
		msg = append(msg, c.Password...)
		if _, err := conn.Write(msg); err != nil {
			return proxyTest{Stage: "auth", Detail: err.Error(), Ms: ms()}
		}
		if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 0x00 {
			return proxyTest{Stage: "auth", Detail: "it refused the configured credentials", Ms: ms()}
		}
		return proxyTest{OK: true, Stage: "auth", Detail: "answers as SOCKS5 and accepts the credentials", Ms: ms()}
	}
	return proxyTest{OK: true, Stage: "handshake", Detail: "answers as SOCKS5, open to anyone who reaches it", Ms: ms()}
}
