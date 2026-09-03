package node

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/utils/network"
	"github.com/xtaci/smux"
)

// Timeouts. A node is on the other side of a link this project exists because
// it is unreliable, so these are generous enough not to drop a healthy node on
// a bad minute and short enough that a dead one does not sit in the fleet list
// pretending to be up.
const (
	handshakeTimeout = 20 * time.Second
	authTimeout      = 15 * time.Second
	callTimeout      = 60 * time.Second
)

// muxSettings are small on purpose. The largest message is one tunnel's form;
// sizing the buffers for a bulk transfer would hold memory per node for
// throughput this channel never carries.
var muxSettings = network.MuxSettings{
	MaxFrameSize:     32 << 10,
	MaxReceiveBuffer: 512 << 10,
	MaxStreamBuffer:  128 << 10,
}

// session is one live node connection.
type session struct {
	node  Node
	mux   *smux.Session
	since time.Time
}

// Hub accepts node connections and sends operations down them.
//
// It holds no queue. An operation is attempted against a node that is connected
// now, and fails immediately if it is not — because the alternative, a command
// that runs at some unspecified later time, means the operator clicks Save and
// finds out an hour later that the far end disagreed with the form they no
// longer have open.
type Hub struct {
	mu   sync.RWMutex
	live map[string]*session

	// lns is one listener per port, and expect says which server each port
	// belongs to. Keeping them apart from the fleet on disk means a port can be
	// opened and closed while the panel runs, which is what adding and removing
	// a server does.
	lns    map[int]net.Listener
	expect map[int]string

	hubKey string
	ctx    context.Context
	onLog  func(string)
}

// NewHub returns a hub. log may be nil.
func NewHub(log func(string)) *Hub {
	if log == nil {
		log = func(string) {}
	}
	return &Hub{live: map[string]*session{}, lns: map[int]net.Listener{},
		expect: map[int]string{}, onLog: log}
}

// Start readies the hub. Nothing is accepted until Open is called for a port.
func (h *Hub) Start(ctx context.Context, hubKey string) error {
	if hubKey == "" {
		return fmt.Errorf("the hub key is not set")
	}
	h.mu.Lock()
	h.ctx, h.hubKey = ctx, hubKey
	h.mu.Unlock()
	go func() { <-ctx.Done(); h.Shutdown() }()
	return nil
}

// Open starts accepting one server on one port.
func (h *Hub) Open(port int, name string) error {
	h.mu.Lock()
	if h.ctx == nil {
		h.mu.Unlock()
		return fmt.Errorf("the hub has not been started")
	}
	if _, ok := h.lns[port]; ok {
		h.mu.Unlock()
		return nil // already listening there
	}
	ctx, key := h.ctx, h.hubKey
	h.mu.Unlock()

	ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return fmt.Errorf("port %d: %w", port, err)
	}
	h.mu.Lock()
	h.lns[port] = ln
	h.expect[port] = name
	h.mu.Unlock()

	go h.accept(ctx, ln, port, name, key)
	return nil
}

// Close stops accepting on one port. A server already connected there stays
// connected: closing the door does not evict whoever came through it, and
// revoking a node is what disconnects one.
func (h *Hub) Close(port int) {
	h.mu.Lock()
	ln := h.lns[port]
	delete(h.lns, port)
	delete(h.expect, port)
	h.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
}

// Shutdown closes every listener and every live session.
func (h *Hub) Shutdown() {
	h.mu.Lock()
	lns := h.lns
	live := h.live
	h.lns = map[int]net.Listener{}
	h.expect = map[int]string{}
	h.live = map[string]*session{}
	h.mu.Unlock()
	for _, ln := range lns {
		ln.Close()
	}
	for _, s := range live {
		s.mux.Close()
	}
}

// Listening reports the ports the hub currently has open.
func (h *Hub) Listening() map[int]string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[int]string, len(h.expect))
	for p, n := range h.expect {
		out[p] = n
	}
	return out
}

// Addr is the address of one open port, or "" if it is not open.
func (h *Hub) Addr(port int) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if ln := h.lns[port]; ln != nil {
		return ln.Addr().String()
	}
	return ""
}

func (h *Hub) accept(ctx context.Context, ln net.Listener, port int, name, key string) {
	for {
		raw, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			h.mu.RLock()
			stillOurs := h.lns[port] == ln
			h.mu.RUnlock()
			if !stillOurs {
				return // the port was closed under us
			}
			// A single failed accept is not a reason to stop accepting: the
			// usual cause is one peer going away between the SYN and here.
			h.onLog("node hub: accept on " + strconv.Itoa(port) + ": " + err.Error())
			continue
		}
		go h.serve(ctx, raw, key, name)
	}
}

func (h *Hub) serve(ctx context.Context, raw net.Conn, hubKey, expect string) {
	defer raw.Close()

	conn, err := network.NoiseServerConn(raw, hubKey, handshakeTimeout)
	if err != nil {
		// Deliberately not logged with the peer's address at anything above
		// debug: an open port on the internet collects handshake failures all
		// day, and a log that fills with them hides the one that matters.
		return
	}

	mux, err := smux.Server(conn, network.SmuxConfig(1, muxSettings))
	if err != nil {
		return
	}
	defer mux.Close()

	// The node speaks first, and has a short window to do it. A connection that
	// completes the Noise handshake and then says nothing is holding a session
	// for free, which is the cheapest way to exhaust a server.
	st, err := mux.AcceptStream()
	if err != nil {
		return
	}
	st.SetDeadline(time.Now().Add(authTimeout))

	n, err := h.authenticate(st, expect)
	st.Close()
	if err != nil {
		h.onLog("node hub: " + err.Error())
		return
	}

	s := &session{node: n, mux: mux, since: time.Now()}
	h.mu.Lock()
	// A node that reconnects before the old session has noticed it is gone
	// would otherwise leave two entries, and commands would go down whichever
	// the map happened to hold. The newest connection wins.
	if old := h.live[n.Name]; old != nil {
		old.mux.Close()
	}
	h.live[n.Name] = s
	h.mu.Unlock()
	h.onLog("node " + n.Name + " connected")

	defer func() {
		h.mu.Lock()
		if h.live[n.Name] == s {
			delete(h.live, n.Name)
		}
		h.mu.Unlock()
		h.onLog("node " + n.Name + " disconnected")
	}()

	go func() { <-ctx.Done(); mux.Close() }()

	// Blocking here is what notices a node going away.
	//
	// Not on CloseChan: smux closes that channel when this side calls Close,
	// and a peer that disconnects trips a socket read error instead — so a hub
	// waiting on CloseChan would hold a dead node in the fleet list until it
	// happened to be sent a command. AcceptStream returns on either.
	//
	// It also puts the protocol the right way round. The node speaks once, to
	// identify itself; everything after that is the panel asking and the node
	// answering. A stream opened from the far end now is a node doing something
	// this design does not have it do, and it is refused in the same words as
	// any other operation that is not on the list.
	for {
		st, err := mux.AcceptStream()
		if err != nil {
			return
		}
		go func() {
			writeMsg(st, Response{Err: errUnknownOp.Error()})
			st.Close()
		}()
	}
}

// authenticate reads the node's opening message and answers it.
//
// expect is the server this port belongs to. Checking it is the point of having
// a port each: a credential is only accepted at its own door, so a key that
// leaks is worth nothing without also knowing which port it was issued for.
func (h *Hub) authenticate(st net.Conn, expect string) (Node, error) {
	var req Request
	if err := readMsg(st, &req); err != nil {
		return Node{}, fmt.Errorf("could not read the node's first message: %w", err)
	}
	switch req.Op {
	case OpEnroll:
		var er enrollRequest
		if err := json.Unmarshal(req.Body, &er); err != nil {
			writeMsg(st, Response{Err: "malformed enrolment"})
			return Node{}, fmt.Errorf("malformed enrolment")
		}
		n, err := Redeem(er.Token, er.Info)
		if err == nil && expect != "" && !strings.EqualFold(n.Name, expect) {
			// The token was valid but arrived at another server's port. The
			// enrolment has already been spent by Redeem, so it is put back the
			// only way it can be — the record is removed and the operator
			// generates a new command.
			_ = Remove(n.Name)
			writeMsg(st, Response{Err: "that setup key belongs to a different server"})
			return Node{}, fmt.Errorf("enrolment for %q arrived on %q's port", n.Name, expect)
		}
		if err != nil {
			writeMsg(st, Response{Err: err.Error()})
			return Node{}, fmt.Errorf("enrolment refused: %w", err)
		}
		body, _ := json.Marshal(enrollResult{NodeKey: n.Key, Name: n.Name})
		if err := writeMsg(st, Response{OK: true, Body: body}); err != nil {
			// The node never learned its key, so the record is unusable and
			// the operator would be left with a node that exists in the panel
			// and cannot connect. Take it back out.
			_ = Remove(n.Name)
			return Node{}, fmt.Errorf("could not hand the node its key: %w", err)
		}
		return n, nil

	case OpAuth:
		var ar authRequest
		if err := json.Unmarshal(req.Body, &ar); err != nil {
			writeMsg(st, Response{Err: "malformed authentication"})
			return Node{}, fmt.Errorf("malformed authentication")
		}
		n, ok := ByKey(ar.NodeKey)
		if ok && expect != "" && !strings.EqualFold(n.Name, expect) {
			writeMsg(st, Response{Err: "this credential is not for this port"})
			return Node{}, fmt.Errorf("node %q authenticated on %q's port", n.Name, expect)
		}
		if !ok {
			writeMsg(st, Response{Err: "this node is not registered — it may have been removed"})
			return Node{}, fmt.Errorf("unknown node key")
		}
		_ = NoteInfo(n.Name, ar.Info)
		n.Info = ar.Info
		writeMsg(st, Response{OK: true})
		return n, nil
	}
	writeMsg(st, Response{Err: errUnknownOp.Error()})
	return Node{}, errUnknownOp
}

// Online lists the names of the nodes currently connected.
func (h *Hub) Online() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(h.live))
	for name := range h.live {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// IsOnline reports whether one node is connected.
func (h *Hub) IsOnline(name string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.live[name]
	return ok
}

// ErrOffline is what every call returns when the node is not connected. The
// wording is the operator's, not the protocol's: "offline" is a fact they can
// act on, where "no session" is not.
type ErrOffline struct{ Name string }

func (e ErrOffline) Error() string {
	return "node " + e.Name + " is not connected"
}

// Call runs one operation on one node and decodes the answer into out.
func (h *Hub) Call(name, op string, body, out any) error {
	h.mu.RLock()
	s := h.live[name]
	h.mu.RUnlock()
	if s == nil {
		return ErrOffline{Name: name}
	}

	st, err := s.mux.OpenStream()
	if err != nil {
		return fmt.Errorf("could not reach %s: %w", name, err)
	}
	defer st.Close()
	st.SetDeadline(time.Now().Add(callTimeout))

	req := Request{Op: op}
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		req.Body = raw
	}
	if err := writeMsg(st, req); err != nil {
		return fmt.Errorf("could not send to %s: %w", name, err)
	}
	var resp Response
	if err := readMsg(st, &resp); err != nil {
		return fmt.Errorf("no answer from %s: %w", name, err)
	}
	if !resp.OK {
		if resp.Err == "" {
			resp.Err = "the node refused the request"
		}
		// The node's own words, unwrapped: it is describing its own machine and
		// nothing here knows better than it does what went wrong there.
		return fmt.Errorf("%s", resp.Err)
	}
	if out != nil && len(resp.Body) > 0 {
		return json.Unmarshal(resp.Body, out)
	}
	return nil
}
