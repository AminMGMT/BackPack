package node

import (
	"encoding/json"
	"errors"
)

// The operations a node will perform. This list is the security boundary: a
// node executes these and refuses everything else, so widening it is the one
// change in this package that has to be argued for rather than made.
//
// Note what is absent. There is no operation that runs a command, reads a path,
// installs a binary, or removes a tunnel. The first three would turn the
// channel into a remote shell. The fourth is left out for a different reason —
// applying a configuration is reversible, because the previous one is filed and
// a failed apply rolls back, and deleting is not. A tunnel that should not
// exist can be stopped from here and removed on the machine.
const (
	// OpHello asks a node to describe itself. The panel calls it on every
	// reconnect, because an address, a kernel or a version can change between
	// one connection and the next and a stale fact on a fleet screen is worse
	// than no fact.
	OpHello = "hello"

	// OpApply sends the complete desired state of one tunnel. It creates the
	// tunnel if it is not there and rewrites it if it is; see ApplyRequest.
	OpApply = "apply"

	// OpList asks which tunnels the node has, and whether they are running.
	OpList = "list"

	// OpStatus asks about one tunnel by name.
	OpStatus = "status"

	// OpSettings reads back one tunnel's settings as the panel's own edit form
	// would show them.
	//
	// It exists because an edit rebuilds the far end from the mirror of this
	// end, and the mirror carries only what the two ends must agree on. The
	// answers that belong to the far end alone — its outbound proxy, the
	// interface or source address it dials from, its backup addresses — were
	// given once, when the tunnel was paired, and are held nowhere on this
	// side. Rebuilding without them would quietly drop them on every edit, so
	// the panel asks the node what it currently has and lays the edit over it.
	OpSettings = "settings"

	// OpLogs returns the far end's journal for one tunnel.
	//
	// A tunnel is one thing in two places and its log is not: half of what went
	// wrong is on the other machine, and reading it meant logging into that
	// machine — which is the second pass the whole fleet feature exists to
	// remove. The panel asks for it the same way it asks for anything else.
	OpLogs = "logs"

	// OpStart, OpStop and OpRestart drive one tunnel's service. Nothing is
	// written by any of them.
	//
	// A tunnel is one tunnel in two places, and its state is one state: an end
	// stopped on its own is not a stopped tunnel, it is a tunnel with one half
	// dialling something that will never answer, retrying for as long as anyone
	// leaves it. So the card's buttons reach both ends, the same way its Edit
	// does.
	OpStart   = "start"
	OpStop    = "stop"
	OpRestart = "restart"

	// OpReceive runs the speed test's receiver for a bounded time.
	//
	// Adding to this list is the one change in this package that has to be
	// argued for, so: a speed test measures by pushing bytes at a sink on the
	// other server, and until now somebody had to go and start that sink by
	// hand — the panel's own error said so, and pointed at a CLI menu on a
	// machine the operator was not sitting at. On a managed server that is
	// exactly the second pass this feature exists to remove.
	//
	// What it grants is narrow. It opens a listener on one port for a few
	// seconds and discards everything that arrives; it reads nothing, writes
	// nothing, and closes itself whether or not anyone connects. The port is
	// one of the tunnel's own backend ports, which the panel already knows
	// because it wrote the configuration.
	OpReceive = "receive"
)

// Request is one operation. Body is the operation's own arguments, left as raw
// JSON so that a panel and a node running different versions can pass a field
// neither of them shares an opinion about.
type Request struct {
	Op   string          `json:"op"`
	Body json.RawMessage `json:"body,omitempty"`
}

// Response is the answer. Err carries a message meant to be shown to the
// operator as-is: it is the node's own words about the node's own machine, and
// rewording it at the panel loses the only description of what actually
// happened.
type Response struct {
	OK   bool            `json:"ok"`
	Err  string          `json:"err,omitempty"`
	Body json.RawMessage `json:"body,omitempty"`
}

// Info is what a node reports about itself.
type Info struct {
	Name     string `json:"name,omitempty"` // the node's name in the panel
	Hostname string `json:"hostname,omitempty"`
	Version  string `json:"version,omitempty"`
	OS       string `json:"os,omitempty"`
	Arch     string `json:"arch,omitempty"`
	IPv4     string `json:"ipv4,omitempty"`
	IPv6     string `json:"ipv6,omitempty"`

	// What the fleet card says about the machine itself.
	//
	// OS above is the kernel's word for the platform — "linux" — which says
	// nothing an operator did not already know. Distro is what the machine
	// calls itself, and Uptime is how long it has been up, which together are
	// the two facts worth looking at a server's card to read when nothing is
	// wrong.
	Distro string `json:"distro,omitempty"`
	Uptime string `json:"uptime,omitempty"`
}

// LogsRequest asks for one tunnel's journal on the far server.
type LogsRequest struct {
	Name  string `json:"name"`
	Lines int    `json:"lines,omitempty"` // 0 means the usual number
}

// LogsResult is what came back.
type LogsResult struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// TunnelState is one tunnel on a node, as the fleet screen shows it.
type TunnelState struct {
	Name    string `json:"name"`
	Kind    string `json:"kind,omitempty"` // reverse, direct or l3
	Service string `json:"service,omitempty"`
	Active  bool   `json:"active"`
	Enabled bool   `json:"enabled"`
}

// ApplyResult says what an apply did.
type ApplyResult struct {
	Service string `json:"service"`
	Active  bool   `json:"active"`
	// Created distinguishes the tunnel that was made from the one that was
	// rewritten, which is the difference between "added" and "updated" in the
	// panel's own wording.
	Created bool `json:"created"`
}

// errUnknownOp is what a node answers to anything not on the list.
var errUnknownOp = errors.New("this panel asked for something this server does not do")
