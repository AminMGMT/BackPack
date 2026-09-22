// Package cli is the non-interactive face of the same operations the menu
// offers.
//
// Everything BackPack does from a terminal has until now gone through
// internal/menu: 1,400 lines that read stdin and write stdout directly. That
// shape has two costs and they are the same cost seen from two sides. Nothing
// can drive it, so nothing tests it — it is the largest package in the tree
// with no test file. And nothing can script it, so the answer to "how do I
// check every server's tunnels from cron" has been "you cannot".
//
// The fix is not to test the menu. It is to give the same operations an entry
// point that has no I/O in it at all:
//
//	Run(args) -> Result{Out, Err, Code}
//
// That is the whole interface. Everything a caller needs to know is in it, the
// behaviour behind it is the same manage package the menu and the panel both
// call, and a test drives it by passing a string slice and reading a struct.
// main.go does the printing and the exiting, which is the only part that
// genuinely needs a process.
//
// It is deliberately thin over manage rather than a second home for logic:
// manage is already the seam the panel and the menu meet at, and a third caller
// that reimplemented any of it would be a third place for the answers to
// diverge. What lives here is what a command line needs and a menu does not —
// parsing arguments, choosing between human and machine output, and deciding
// an exit code.
package cli

import (
	"encoding/json"
	"fmt"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/tunhist"
	"sort"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
)

// Result is everything a command produced: what to print, what to print on
// stderr, and what to exit with.
//
// Returned rather than written so the whole surface is testable without a
// process, a pipe or a captured os.Stdout.
type Result struct {
	Out  string
	Err  string
	Code int
}

func ok(out string) Result { return Result{Out: out} }

func fail(code int, format string, a ...any) Result {
	return Result{Err: fmt.Sprintf(format, a...), Code: code}
}

// Exit codes. Distinguished so a script can tell "you asked wrongly" from "the
// thing you asked about is unhealthy", which is the distinction cron cares
// about.
const (
	CodeOK        = 0
	CodeUsage     = 2
	CodeNotFound  = 3
	CodeUnhealthy = 4
	CodeFailed    = 1
)

const usage = `backpack — non-interactive commands

  backpack tunnel list [--json]         every tunnel and its state
  backpack tunnel status <name> [--json]  one tunnel: state, peer, and what it has carried
  backpack check -c <file>              validate a config without starting it
  backpack version [--json]

Run backpack with no arguments for the interactive menu.
Exit codes: 0 ok, 1 failed, 2 usage, 3 not found, 4 unhealthy.
`

// Run performs one command. It touches no files of its own and prints nothing;
// everything it did is in the Result.
func Run(args []string) Result {
	// --json is a flag and a flag works wherever it is written, including
	// before the command. Lifted out here and put back on the tail so the
	// handlers below still see it exactly as they did.
	asJSON, args := takeJSONFlag(args)
	if len(args) == 0 {
		return Result{Out: usage, Code: CodeUsage}
	}
	if asJSON {
		args = append(append([]string{}, args...), "--json")
	}
	switch args[0] {
	case "tunnel":
		return runTunnel(args[1:])
	case "check":
		return runCheck(args[1:])
	case "version":
		return runVersion(args[1:])
	case "help", "-h", "--help":
		return ok(usage)
	}
	return fail(CodeUsage, "unknown command %q\n\n%s", args[0], usage)
}

func runTunnel(args []string) Result {
	if len(args) == 0 {
		return fail(CodeUsage, "tunnel needs a subcommand: list or status\n")
	}
	asJSON, rest := takeJSONFlag(args[1:])
	switch args[0] {
	case "list":
		if len(rest) != 0 {
			return fail(CodeUsage, "tunnel list takes no arguments\n")
		}
		return tunnelList(asJSON)
	case "status":
		if len(rest) != 1 {
			return fail(CodeUsage, "tunnel status needs exactly one tunnel name\n")
		}
		return tunnelStatus(rest[0], asJSON)
	}
	return fail(CodeUsage, "unknown tunnel subcommand %q\n", args[0])
}

// tunnelView is one tunnel as this package reports it. A named type rather than
// a map so the JSON shape is something a script can rely on across versions.
type tunnelView struct {
	Name      string   `json:"name"`
	Role      string   `json:"role"`
	Transport string   `json:"transport"`
	Addr      string   `json:"addr"`
	Ports     []string `json:"ports,omitempty"`
	State     string   `json:"state"`
	Detail    string   `json:"detail,omitempty"`

	// Traffic is what the tunnel has actually carried, when the engine has
	// written a snapshot recently enough to mean anything.
	//
	// It was not here, and writing the troubleshooting runbook is what made
	// that obvious: the first question about a tunnel that looks healthy is
	// whether it is *moving* anything, and the second is which direction has
	// stopped. Neither could be answered from a terminal. "state: online" is
	// exactly the answer that is wrong in the failure this product cares about
	// most — a tunnel that holds its control channel and carries nothing.
	Traffic *trafficView `json:"traffic,omitempty"`

	// LocalService is the last hop, when the client has found it failing. The
	// tunnel can be perfectly healthy and every connection die one step past
	// the end of it, and this is the only place that says so.
	LocalService string `json:"local_service,omitempty"`

	// Uptime over the last week, when there is enough history to say. Absent
	// rather than zero for a tunnel nothing has sampled yet: not measured is
	// not the same as down, and a young tunnel reported at 3% is a number
	// somebody will act on.
	Uptime *uptimeView `json:"uptime,omitempty"`
}

// uptimeView carries the percentage with the number of checks behind it.
//
// Both, always. A percentage without its sample size is how "100% uptime" comes
// to mean "we have looked once" — and this is the figure an operator repeats to
// whoever is paying them.
type uptimeView struct {
	Percent float64 `json:"percent"`
	Checks  int     `json:"checks"`
	Window  string  `json:"window"`
}

// trafficView is the tunnel's counters, plus the derived answer the counters
// are read for.
type trafficView struct {
	BytesIn  uint64 `json:"bytes_in"`
	BytesOut uint64 `json:"bytes_out"`
	// Peer is the far end, when the transport knows it.
	Peer string `json:"peer,omitempty"`
	// Age is how long ago the engine wrote these, in seconds. A reading nobody
	// can date is a reading nobody can use.
	Age int64 `json:"age_seconds"`
}

func viewOf(t manage.Tunnel, h manage.Health) tunnelView {
	v := tunnelView{
		Name: t.Name, Role: t.Role, Transport: t.Transport, Addr: t.Addr,
		Ports: manage.VisiblePorts(t.Ports, manage.TunnelToken(t.Name)),
		State: h.State, Detail: h.Detail,
	}
	attachTraffic(&v, t.Name)
	attachUptime(&v, t.Name)
	return v
}

// trafficWindow is how old a snapshot may be and still be reported.
//
// Longer than the engine's write interval by enough to survive a slow machine,
// short enough that a stopped tunnel's last reading is not presented as
// current. An older one is left out entirely rather than shown with a caveat:
// a figure on the screen is read as now.
const trafficWindow = 2 * time.Minute

// stateDir is where the engines leave their snapshots. A variable rather than
// app.ConfigDir directly so a test can point it at a temp directory — the
// alternative is a status command that can only be tested on a machine that is
// actually running tunnels.
var stateDir = app.ConfigDir

func attachTraffic(v *tunnelView, name string) {
	snap, err := metrics.Read(stateDir, name)
	if err != nil {
		return
	}
	age := time.Since(snap.Taken)
	if age > trafficWindow || age < 0 {
		return
	}
	v.Traffic = &trafficView{
		BytesIn: snap.BytesIn, BytesOut: snap.BytesOut,
		Peer: snap.Peer, Age: int64(age.Seconds()),
	}
	// The last hop, named the way an operator can act on it: which address,
	// what shape of failure, and how many connections have died that way. A
	// refusal is a service that is not running; a timeout is usually a firewall
	// on the same machine, and the two have different fixes.
	if ls := snap.LocalService; ls != nil && ls.Why != "" {
		v.LocalService = fmt.Sprintf("%s %s (%d failed)", ls.Addr, ls.Why, ls.Failures)
	}
}

// uptimeWindow is what `tunnel status` reports over.
//
// A week rather than a month: it is long enough to cover a bad night and short
// enough that a tunnel fixed on Monday does not read as broken all week. The
// JSON says which window it is, so a script is never guessing.
const uptimeWindow = 7 * 24 * time.Hour

func attachUptime(v *tunnelView, name string) {
	pct, checks, ok := tunhist.UptimeOf(name, uptimeWindow)
	if !ok {
		return
	}
	v.Uptime = &uptimeView{Percent: pct, Checks: checks, Window: "7d"}
}

func tunnelList(asJSON bool) Result {
	tunnels := manage.List()
	health := manage.AllHealth()

	views := make([]tunnelView, 0, len(tunnels))
	for _, t := range tunnels {
		views = append(views, viewOf(t, health[t.Name]))
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })

	if asJSON {
		return jsonResult(views)
	}
	if len(views) == 0 {
		return ok("no tunnels configured\n")
	}
	var b strings.Builder
	for _, v := range views {
		fmt.Fprintf(&b, "%-20s %-7s %-9s %-24s %s\n", v.Name, v.Role, v.Transport, v.Addr, v.State)
	}
	return ok(b.String())
}

func tunnelStatus(name string, asJSON bool) Result {
	t, found := manage.Find(name)
	if !found {
		return fail(CodeNotFound, "no tunnel named %q\n", name)
	}
	v := viewOf(t, manage.TunnelHealth(t))

	r := Result{}
	if asJSON {
		r = jsonResult(v)
	} else {
		var b strings.Builder
		fmt.Fprintf(&b, "name      %s\nrole      %s\ntransport %s\naddress   %s\nstate     %s\n",
			v.Name, v.Role, v.Transport, v.Addr, v.State)
		if v.Detail != "" {
			fmt.Fprintf(&b, "detail    %s\n", v.Detail)
		}
		if len(v.Ports) > 0 {
			fmt.Fprintf(&b, "ports     %s\n", strings.Join(v.Ports, ", "))
		}
		if t := v.Traffic; t != nil {
			if t.Peer != "" {
				fmt.Fprintf(&b, "peer      %s\n", t.Peer)
			}
			fmt.Fprintf(&b, "carried   %s in, %s out (%ds ago)\n",
				humanBytes(t.BytesIn), humanBytes(t.BytesOut), t.Age)
		}
		if v.LocalService != "" {
			fmt.Fprintf(&b, "last hop  %s\n", v.LocalService)
		}
		if u := v.Uptime; u != nil {
			// The sample count is printed, not just the percentage. "100%
			// over 12 checks" and "100% over 2,016" are different claims.
			fmt.Fprintf(&b, "uptime    %.2f%% over %s (%d checks)\n",
				u.Percent, u.Window, u.Checks)
		}
		r = ok(b.String())
	}
	// The exit code carries the answer as well as the output, so `backpack
	// tunnel status x >/dev/null || alert` is a whole monitoring integration.
	if v.State != "online" {
		r.Code = CodeUnhealthy
	}
	return r
}

// runCheck validates a config file without starting anything.
//
// The gap it closes: the engine validates thoroughly at load and does it by
// exiting, which is right for a supervisor and useless for somebody who has
// just hand-edited a file and would like to know *before* restarting a tunnel
// that currently works. The reload path makes that worse in the other
// direction — a file that does not parse is ignored and the tunnel keeps
// running on the old one, quietly.
func runCheck(args []string) Result {
	asJSON, rest := takeJSONFlag(args)
	var path string
	for i := 0; i < len(rest); i++ {
		if rest[i] == "-c" || rest[i] == "--config" {
			if i+1 >= len(rest) {
				return fail(CodeUsage, "-c needs a file\n")
			}
			path = rest[i+1]
			i++
			continue
		}
		if path == "" {
			path = rest[i] // `backpack check file.toml` as well as `-c file.toml`
			continue
		}
		return fail(CodeUsage, "check takes one config file\n")
	}
	if path == "" {
		return fail(CodeUsage, "check needs a config file: backpack check -c /etc/backpack/x.toml\n")
	}

	problems := manage.ValidateConfigFile(path)

	if asJSON {
		r := jsonResult(struct {
			File     string   `json:"file"`
			OK       bool     `json:"ok"`
			Problems []string `json:"problems"`
		}{path, len(problems) == 0, problems})
		if len(problems) > 0 {
			r.Code = CodeFailed
		}
		return r
	}
	if len(problems) == 0 {
		return ok(path + ": looks valid\n" +
			"(checks that need this machine — raw sockets, interfaces, iptables — are " +
			"made by the engine when the tunnel starts)\n")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d problem(s)\n", path, len(problems))
	for _, p := range problems {
		fmt.Fprintf(&b, "  - %s\n", p)
	}
	return Result{Err: b.String(), Code: CodeFailed}
}

func runVersion(args []string) Result {
	asJSON, rest := takeJSONFlag(args)
	if len(rest) != 0 {
		return fail(CodeUsage, "version takes no arguments\n")
	}
	if asJSON {
		return jsonResult(struct {
			Version     string `json:"version"`
			Attribution string `json:"attribution"`
			Source      string `json:"source"`
			Licence     string `json:"licence"`
		}{app.Version, app.Attribution, app.AttributionURL, "AGPL-3.0"})
	}
	return ok(app.Version + "\n" + app.Attribution + "\n" + app.AttributionURL + "\n")
}

func jsonResult(v any) Result {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fail(CodeFailed, "could not render the answer: %v\n", err)
	}
	return ok(string(b) + "\n")
}

// takeJSONFlag pulls --json out of an argument list wherever it appears, so it
// can be written before or after the thing it applies to.
func takeJSONFlag(args []string) (bool, []string) {
	out := make([]string, 0, len(args))
	found := false
	for _, a := range args {
		if a == "--json" || a == "-json" {
			found = true
			continue
		}
		out = append(out, a)
	}
	return found, out
}

// IsCommand reports whether a first argument belongs to this package.
//
// main.go asks before the flag package sees the arguments, because a
// subcommand with arguments of its own would otherwise be reported as an
// unknown flag. Keeping the list here rather than in main is what stops the two
// drifting — a command added below is routable immediately.
func IsCommand(s string) bool {
	switch s {
	case "tunnel", "check", "version":
		return true
	}
	return false
}

// humanBytes renders a counter the way somebody reading a terminal wants it.
//
// Exact bytes are the right thing in JSON, where something is going to do
// arithmetic on them, and the wrong thing on a screen, where the question is
// "is this number big" and 1503238553 does not answer it.
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTP"[exp])
}
