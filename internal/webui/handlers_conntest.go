package webui

// The Connection Test, from the browser.
//
// The menu's Connection Test (main menu → 0) is the one tool that says which
// transports survive the path between two particular servers, and it only
// existed in a terminal. The panel runs on the Iran server, which is the side
// that starts a test and judges it, so this is that side: start the test
// tunnels, hand the operator the one line to paste on the kharej, wait for it
// to check in, and show the rows filling as each tunnel is tried.
//
// It is the menu's own sequence — StartConnTestIran, Joined, Run, Fetched,
// Close — run in the background and read back by polling, because a test
// takes minutes and a request must not.

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/manage"
)

// conntestState is where a test is.
const (
	ctIdle     = "idle"
	ctStarting = "starting"
	ctWaiting  = "waiting" // for the kharej to check in
	ctRunning  = "running"
	ctFinished = "done"
	ctFailed   = "failed"
	ctStopped  = "stopped"
)

// conntestRow is one tunnel under test, as the browser draws it.
type conntestRow struct {
	Kind       string  `json:"kind"`
	Tr         string  `json:"tr"`
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	Detail     string  `json:"detail,omitempty"`
	OK         int     `json:"ok"`
	Tried      int     `json:"tried"`
	Total      int     `json:"total"`
	RTTms      int     `json:"rtt,omitempty"`
	Mbps       float64 `json:"mbps,omitempty"`
	UploadMbps float64 `json:"uploadMbps,omitempty"`
	Phase      string  `json:"phase,omitempty"`
	Connect    float64 `json:"connect,omitempty"`
}

// conntestView is the whole state, as GET /api/conntest answers it.
type conntestView struct {
	State    string               `json:"state"`
	Host     string               `json:"host,omitempty"`
	Preset   string               `json:"preset,omitempty"`
	Link     string               `json:"link,omitempty"`
	Command  string               `json:"command,omitempty"`
	Install  string               `json:"install,omitempty"`
	Kharej   string               `json:"kharej,omitempty"`
	Error    string               `json:"error,omitempty"`
	Started  int64                `json:"started,omitempty"`
	Deadline int64                `json:"deadline,omitempty"` // unix: when waiting gives up
	Ran      int64                `json:"ran,omitempty"`      // unix: when the kharej joined and the test began
	Ends     int64                `json:"ends,omitempty"`     // unix: about when the running test is judged
	Finished int64                `json:"finished,omitempty"`
	Rows     []conntestRow        `json:"rows,omitempty"`
	Best     *manage.ConnTestBest `json:"best,omitempty"`
	// What the form starts from.
	DefaultHost string `json:"defaultHost,omitempty"`
	Root        bool   `json:"root"`
}

// conntestRunner holds the one test this panel can run at a time. Two at once
// would measure each other, which is the reason the menu runs one too.
type conntestRunner struct {
	mu     sync.Mutex
	v      conntestView
	cancel context.CancelFunc
}

func (c *conntestRunner) view() conntestView {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := c.v
	v.Rows = append([]conntestRow(nil), c.v.Rows...)
	if v.State == "" {
		v.State = ctIdle
	}
	v.Root = os.Geteuid() == 0
	return v
}

func (c *conntestRunner) update(f func(v *conntestView)) {
	c.mu.Lock()
	f(&c.v)
	c.mu.Unlock()
}

func (c *conntestRunner) busy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.v.State {
	case ctStarting, ctWaiting, ctRunning:
		return true
	}
	return false
}

func (c *conntestRunner) stop() {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func rowOf(r manage.ConnTestResult) conntestRow {
	return conntestRow{
		Kind: r.Kind, Tr: r.Transport, Name: manage.ConnTestName(r.Transport),
		Status: r.Status, Detail: r.Detail, OK: r.OK, Tried: r.Tried, Total: r.Total,
		RTTms: r.RTTms, Mbps: r.Mbps, Connect: r.Connect,
		UploadMbps: r.UploadMbps, Phase: r.Phase,
	}
}

// start begins a test in the background and returns once the test tunnels are
// up and the link exists, or they could not be started.
func (c *conntestRunner) start(parent context.Context, host, preset string) error {
	ctx, cancel := context.WithCancel(parent)
	c.mu.Lock()
	c.cancel = cancel
	c.v = conntestView{State: ctStarting, Host: host, Preset: preset, Started: time.Now().Unix()}
	c.mu.Unlock()

	s, link, err := manage.StartConnTestIran(manage.ConnTestOptions{
		Host: host, Preset: preset,
		// The menu includes what needs root only with root; the panel runs as
		// root on a real server, so this is normally everything.
		Direct: os.Geteuid() == 0, SpoofSrc: manage.ConnTestSpoofSource,
	})
	if err != nil {
		cancel()
		c.update(func(v *conntestView) { v.State, v.Error, v.Finished = ctFailed, err.Error(), time.Now().Unix() })
		return err
	}
	wait := manage.ConnTestJoinWait()
	c.update(func(v *conntestView) {
		v.State, v.Link = ctWaiting, link
		v.Command = "sudo backpack link apply '" + link + "'"
		v.Install = manage.InstallCommand(link)
		v.Deadline = time.Now().Add(wait).Unix()
	})

	go func() {
		defer cancel()
		defer s.Close()
		finish := func(state, why string) {
			c.update(func(v *conntestView) {
				v.State, v.Error, v.Finished = state, why, time.Now().Unix()
			})
		}
		select {
		case <-s.Joined():
		case <-ctx.Done():
			finish(ctStopped, "")
			return
		case <-time.After(wait):
			finish(ctFailed, "the kharej never checked in — nothing reached this server's test port, neither over TCP nor UDP")
			return
		}
		c.update(func(v *conntestView) {
			v.State, v.Kharej = ctRunning, s.Kharej()
			v.Ran = time.Now().Unix()
			v.Ends = time.Now().Add(manage.ConnTestRunTime()).Unix()
		})

		results := s.Run(ctx, func(i int, r manage.ConnTestResult) {
			c.update(func(v *conntestView) {
				for len(v.Rows) <= i {
					v.Rows = append(v.Rows, conntestRow{})
				}
				v.Rows[i] = rowOf(r)
				if r.Phase == "speed" {
					pending := 0
					for _, row := range v.Rows {
						if row.Phase == "speed" || row.Phase == "speed-queued" {
							pending++
						}
					}
					v.Ends = time.Now().Add(manage.ConnTestSpeedTime(pending) + 15*time.Second).Unix()
				}
			})
		})
		best := s.Best()
		c.update(func(v *conntestView) {
			v.Rows = v.Rows[:0]
			for _, r := range results {
				v.Rows = append(v.Rows, rowOf(r))
			}
			if best.Transport != "" {
				v.Best = &best
			}
		})
		if ctx.Err() != nil {
			finish(ctStopped, "")
			return
		}
		finish(ctFinished, "")
		// The kharej collects the verdict on its next question, a few seconds
		// away; the tunnels stay up until then so it is not told they failed.
		select {
		case <-s.Fetched():
		case <-ctx.Done():
		case <-time.After(30 * time.Second):
		}
	}()
	return nil
}

// handleConnTest is GET for the state, POST action=start|stop.
func (s *server) handleConnTest(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		v := s.conntest.view()
		v.DefaultHost = manage.ConnTestHost()
		writeJSON(w, v)
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, "could not read the request", http.StatusBadRequest)
			return
		}
		switch r.FormValue("action") {
		case "start":
			if s.conntest.busy() {
				http.Error(w, "a connection test is already running — stop it first", http.StatusConflict)
				return
			}
			host := strings.Trim(strings.TrimSpace(r.FormValue("host")), "[]")
			if host == "" {
				host = manage.ConnTestHost()
			}
			if host == "" {
				http.Error(w, "this server's address is needed: it is what the kharej dials", http.StatusBadRequest)
				return
			}
			preset := strings.ToLower(strings.TrimSpace(r.FormValue("preset")))
			ctx := s.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			if err := s.conntest.start(ctx, host, preset); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, s.conntest.view())
		case "stop":
			s.conntest.stop()
			writeJSON(w, s.conntest.view())
		default:
			http.Error(w, "action must be start or stop", http.StatusBadRequest)
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
