package node

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/xtaci/smux"
)

// AgentPath is where a managed server keeps its side: which panel to call, the
// hub key to reach it with, and the credential it was issued.
var AgentPath = app.ConfigDir + "/node-agent.json"

// AgentConfig is that file.
type AgentConfig struct {
	// Server is the panel, host:port.
	Server string `json:"server"`

	// HubKey is the Noise pre-shared key for the channel.
	HubKey string `json:"hubKey"`

	// Enroll is the single-use token, present only until it has been redeemed.
	// It is cleared on success rather than kept "in case", because a token that
	// is still on disk after it has been spent is a secret with no purpose.
	Enroll string `json:"enroll,omitempty"`

	// NodeKey is the credential this server was issued at enrolment. Its
	// presence is what distinguishes an enrolled node from one that has never
	// connected.
	NodeKey string `json:"nodeKey,omitempty"`

	Name string `json:"name,omitempty"`
}

// LoadAgent reads the agent config.
func LoadAgent() (AgentConfig, error) {
	var c AgentConfig
	data, err := os.ReadFile(AgentPath)
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(data, &c)
}

// SaveAgent persists it, root-only: it holds the credential.
func SaveAgent(c AgentConfig) error {
	data, _ := json.MarshalIndent(c, "", "  ")
	return app.WriteFileAtomic(AgentPath, data, 0600)
}

// SetupKey is the one string an operator pastes: the hub key and the enrolment
// token, joined.
//
// They are one field because they are always copied together and separating
// them would only create a way to paste half of a pair. The dot is safe because
// both halves are hex.
func SetupKey(hub, enroll string) string { return hub + "." + enroll }

// ParseSetupKey splits it back.
func ParseSetupKey(key string) (hub, enroll string, err error) {
	hub, enroll, ok := strings.Cut(strings.TrimSpace(key), ".")
	if !ok || hub == "" || enroll == "" {
		return "", "", fmt.Errorf("that setup key is not in the expected form")
	}
	return hub, enroll, nil
}

// Backoff between connection attempts. It starts short because the common
// reason to be disconnected is a panel that has just restarted, and grows
// because the other common reason is a panel that is not coming back for a
// while and should not be dialled once a second until it does.
const (
	dialMin = 2 * time.Second
	dialMax = 60 * time.Second
)

// Agent is the managed server's side of the channel.
type Agent struct {
	cfg   AgentConfig
	onLog func(string)
}

// NewAgent returns an agent for a config. log may be nil.
func NewAgent(cfg AgentConfig, log func(string)) *Agent {
	if log == nil {
		log = func(string) {}
	}
	return &Agent{cfg: cfg, onLog: log}
}

// Run keeps the agent connected until ctx ends.
//
// It never returns an error for a connection that failed. A node whose panel is
// unreachable is not a broken node — it is a node with nothing to do — and
// exiting would mean systemd restarting the process on a timer that is worse
// than the one here, and losing the enrolment state in flight.
func (a *Agent) Run(ctx context.Context) {
	wait := dialMin
	for {
		if ctx.Err() != nil {
			return
		}
		err := a.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			a.onLog("node agent: " + err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait *= 2
		if wait > dialMax {
			wait = dialMax
		}
	}
}

// session runs one connection from dial to close.
func (a *Agent) session(ctx context.Context) error {
	if a.cfg.Server == "" || a.cfg.HubKey == "" {
		return fmt.Errorf("this server is not set up as a node")
	}

	d := net.Dialer{Timeout: handshakeTimeout}
	raw, err := d.DialContext(ctx, "tcp", a.cfg.Server)
	if err != nil {
		return fmt.Errorf("could not reach the panel at %s: %w", a.cfg.Server, err)
	}
	defer raw.Close()

	conn, err := network.NoiseClientConn(raw, a.cfg.HubKey, handshakeTimeout)
	if err != nil {
		// The hub key not matching looks exactly like this, and it is by far
		// the likeliest cause, so say so rather than reporting a handshake
		// failure the operator cannot act on.
		return fmt.Errorf("the panel refused the handshake — check the setup key: %w", err)
	}

	mux, err := smux.Client(conn, network.SmuxConfig(1, muxSettings))
	if err != nil {
		return err
	}
	defer mux.Close()

	// Nothing below reads ctx: the session blocks in AcceptStream, which only
	// returns when the connection ends. Without this the agent would stay
	// connected through its own shutdown — the process would be told to stop
	// and go on answering the panel until the socket happened to break.
	stop := make(chan struct{})
	defer close(stop)
	identified := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			// Shutdown waits for identify. Enrolment is the one moment that
			// must not be cut short: the panel spends the setup token before
			// it answers, so a node torn down between the panel's reply and
			// SaveAgent below would hold no credential and could never enrol
			// again — bricked by nothing worse than being stopped at the wrong
			// instant. identify carries its own deadline, so this cannot hang.
			<-identified
			mux.Close()
		case <-stop:
		}
	}()

	err = a.identify(mux)
	close(identified)
	if err != nil {
		return err
	}
	a.onLog("connected to the panel at " + a.cfg.Server)

	// From here the panel drives. Every stream it opens is one operation.
	for {
		st, err := mux.AcceptStream()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("the connection to the panel ended: %w", err)
		}
		go a.handle(st)
	}
}

// identify enrols or authenticates, whichever this node still needs.
func (a *Agent) identify(mux *smux.Session) error {
	st, err := mux.OpenStream()
	if err != nil {
		return err
	}
	defer st.Close()
	st.SetDeadline(time.Now().Add(authTimeout))

	info := LocalInfo()
	info.Name = a.cfg.Name

	if a.cfg.NodeKey == "" {
		body, _ := json.Marshal(enrollRequest{Token: a.cfg.Enroll, Info: info})
		if err := writeMsg(st, Request{Op: OpEnroll, Body: body}); err != nil {
			return err
		}
		var resp Response
		if err := readMsg(st, &resp); err != nil {
			return err
		}
		if !resp.OK {
			return fmt.Errorf("the panel refused this server: %s", resp.Err)
		}
		var res enrollResult
		if err := json.Unmarshal(resp.Body, &res); err != nil {
			return err
		}
		a.cfg.NodeKey = res.NodeKey
		a.cfg.Name = res.Name
		a.cfg.Enroll = ""
		// Saved before anything else happens on this connection. The panel has
		// already spent the token, so a node that had the key only in memory
		// and then restarted would be locked out with no way back.
		if err := SaveAgent(a.cfg); err != nil {
			return fmt.Errorf("enrolled, but could not save the credential: %w", err)
		}
		// Now that the key is on disk, tell the panel so it can retire the
		// setup token. Failing to say so is not worth abandoning the session
		// for — the key is saved either way, and the panel retires the token
		// on the first authentication regardless.
		_ = writeMsg(st, Request{Op: OpEnrolled})
		a.onLog("enrolled with the panel as " + res.Name)
		return nil
	}

	body, _ := json.Marshal(authRequest{NodeKey: a.cfg.NodeKey, Info: info})
	if err := writeMsg(st, Request{Op: OpAuth, Body: body}); err != nil {
		return err
	}
	var resp Response
	if err := readMsg(st, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("the panel refused this server: %s", resp.Err)
	}
	return nil
}

// handle answers one operation.
func (a *Agent) handle(st net.Conn) {
	defer st.Close()
	st.SetDeadline(time.Now().Add(callTimeout))

	var req Request
	if err := readMsg(st, &req); err != nil {
		return
	}
	resp := Execute(req)
	if !resp.OK {
		a.onLog("node agent: " + req.Op + ": " + resp.Err)
	}
	_ = writeMsg(st, resp)
}
