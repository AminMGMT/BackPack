package node

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
)

// ApplyRequest is the complete desired state of one tunnel.
//
// It carries the panel's own setup form rather than a rendered config file. The
// panel could render the TOML and send that — it is the same binary at both
// ends — but then the node would be writing a file it had not checked, and
// every validation the form path performs (the port that is already taken, the
// preset that does not suit the transport, the certificate a WSS server needs)
// would happen on the wrong machine, against the wrong machine's facts.
//
// Sending the form means the node builds its own config from it, so those
// checks run where their answers are true.
type ApplyRequest struct {
	Kind   string                  `json:"kind"` // "reverse" or "direct"
	Tunnel *manage.NewTunnel       `json:"tunnel,omitempty"`
	Direct *manage.NewDirectTunnel `json:"direct,omitempty"`
}

// ReceiveRequest asks for the speed test's sink: which port, and for how long.
type ReceiveRequest struct {
	Port    int `json:"port"`
	Seconds int `json:"seconds"`
}

// NameRequest addresses one tunnel.
type NameRequest struct {
	Name string `json:"name"`
}

// Execute performs one operation and returns the answer.
//
// Everything not on the list is refused here rather than at the panel. A node
// that trusted the panel to send only sensible operations would be a node that
// does whatever anything holding the key asks, and the point of the whole
// design is that it does not.
func Execute(req Request) Response {
	switch req.Op {
	case OpHello:
		return okBody(LocalInfo())

	case OpApply:
		var ar ApplyRequest
		if err := json.Unmarshal(req.Body, &ar); err != nil {
			return failf("the panel sent an apply this build cannot read: %v", err)
		}
		return doApply(ar)

	case OpList:
		return okBody(localTunnels())

	case OpStatus:
		var nr NameRequest
		if err := json.Unmarshal(req.Body, &nr); err != nil {
			return failf("malformed status request")
		}
		for _, t := range localTunnels() {
			if strings.EqualFold(t.Name, nr.Name) {
				return okBody(t)
			}
		}
		return failf("no tunnel named %q on this server", nr.Name)

	case OpSettings:
		var nr NameRequest
		if err := json.Unmarshal(req.Body, &nr); err != nil {
			return failf("malformed settings request")
		}
		set, err := manage.TunnelSettingsOf(nr.Name)
		if err != nil {
			return failf("%v", err)
		}
		return okBody(set)

	case OpReceive:
		var rr ReceiveRequest
		if err := json.Unmarshal(req.Body, &rr); err != nil {
			return failf("malformed receive request")
		}
		return doReceive(rr)

	case OpStart, OpStop, OpRestart:
		var nr NameRequest
		if err := json.Unmarshal(req.Body, &nr); err != nil {
			return failf("malformed %s request", req.Op)
		}
		t, ok := manage.Find(nr.Name)
		if !ok {
			return failf("no tunnel named %q on this server", nr.Name)
		}
		var err error
		switch req.Op {
		case OpStart:
			err = manage.StartService(t.Service)
		case OpStop:
			err = manage.StopService(t.Service)
		default:
			err = manage.RestartService(t.Service)
		}
		if err != nil {
			return failf("%v", err)
		}
		return okBody(TunnelState{
			Name:    t.Name,
			Service: t.Service,
			Active:  manage.IsActive(t.Service),
			Enabled: manage.IsEnabled(t.Service),
		})
	}
	return fail(errUnknownOp.Error())
}

// receiveWindow caps how long the sink may run.
//
// A listener that outlives the measurement is a listener nobody asked for, and
// the whole point of doing this from here is that nobody has to remember to
// stop it. The ceiling is a little over the longest measurement the panel will
// run, so a slow start still finishes and a forgotten one still ends.
const receiveWindow = 40 * time.Second

// doReceive runs the speed test's sink on one port for a bounded time.
//
// It returns as soon as the listener is up rather than when it closes: the
// panel has to start measuring while it is running, and a call that only
// answered at the end would be answering after the thing it enabled was over.
func doReceive(rr ReceiveRequest) Response {
	if rr.Port < 1 || rr.Port > 65535 {
		return failf("port %d is not a port", rr.Port)
	}
	d := time.Duration(rr.Seconds) * time.Second
	if d <= 0 || d > receiveWindow {
		d = receiveWindow
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(rr.Port)))
	if err != nil {
		return failf("could not listen on %d: %v", rr.Port, err)
	}
	go func() {
		defer ln.Close()
		done := time.After(d)
		go func() { <-done; ln.Close() }()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Sunk, not echoed — the same rule as ServeThroughputOn, for the
			// same reason: echoing would measure the round trip.
			go func() { defer c.Close(); io.Copy(io.Discard, c) }()
		}
	}()
	return okBody(map[string]any{"port": rr.Port, "seconds": int(d / time.Second)})
}

func doApply(ar ApplyRequest) Response {
	var (
		service string
		active  bool
		created bool
		err     error
	)
	switch ar.Kind {
	case "direct":
		if ar.Direct == nil {
			return fail("the apply says direct but carries no direct form")
		}
		service, active, created, err = manage.ApplyDirectTunnel(*ar.Direct)
	case "reverse", "":
		if ar.Tunnel == nil {
			return fail("the apply carries no tunnel form")
		}
		service, active, created, err = manage.ApplyTunnel(*ar.Tunnel)
	default:
		return failf("unknown tunnel kind %q", ar.Kind)
	}
	if err != nil {
		// Reported as a failure with the node's own wording. The tunnel may
		// well still be running on the previous config — that is what the
		// rollback is for — and saying so is the difference between an
		// operator retrying and an operator panicking.
		return failf("%v", err)
	}
	return okBody(ApplyResult{Service: service, Active: active, Created: created})
}

// addrCache holds the last public addresses this machine reported.
//
// Discovering them means asking a handful of remote services, each with its own
// timeout, which on a server with no route out takes the better part of a
// minute. That is fine for a page the operator has just asked to load, and not
// fine here: LocalInfo is called on every reconnect, and a node that spent
// fifty seconds looking up its own address before it could say hello would
// spend its life reconnecting on links that drop.
//
// So the addresses are cached, refreshed in the background when stale, and
// whatever is known now is what gets reported. A blank address on the first
// connection of a fresh install is the correct trade: the fleet screen fills in
// a few seconds later, and the channel came up immediately.
var addrCache struct {
	sync.Mutex
	v4, v6  string
	at      time.Time
	running bool
}

const addrTTL = 10 * time.Minute

func cachedAddrs() (v4, v6 string) {
	addrCache.Lock()
	defer addrCache.Unlock()
	v4, v6 = addrCache.v4, addrCache.v6
	if time.Since(addrCache.at) < addrTTL || addrCache.running {
		return v4, v6
	}
	addrCache.running = true
	go func() {
		a, b := manage.PublicIPv4(), manage.PublicIPv6()
		addrCache.Lock()
		addrCache.v4, addrCache.v6 = a, b
		addrCache.at = time.Now()
		addrCache.running = false
		addrCache.Unlock()
	}()
	return v4, v6
}

// LocalInfo describes this machine.
func LocalInfo() Info {
	host, _ := os.Hostname()
	v4, v6 := cachedAddrs()
	return Info{
		Hostname: host,
		Version:  app.Version,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		IPv4:     v4,
		IPv6:     v6,
	}
}

func localTunnels() []TunnelState {
	all := manage.List()
	out := make([]TunnelState, 0, len(all))
	for _, t := range all {
		kind := "reverse"
		if manage.IsDirectKind(t) {
			kind = "direct"
		}
		out = append(out, TunnelState{
			Name:    t.Name,
			Kind:    kind,
			Service: t.Service,
			Active:  manage.IsActive(t.Service),
			Enabled: manage.IsEnabled(t.Service),
		})
	}
	return out
}

func okBody(v any) Response {
	raw, err := json.Marshal(v)
	if err != nil {
		return failf("could not encode the answer: %v", err)
	}
	return Response{OK: true, Body: raw}
}

func fail(msg string) Response { return Response{Err: msg} }

func failf(format string, a ...any) Response {
	return Response{Err: fmt.Sprintf(format, a...)}
}
