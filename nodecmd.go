package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"github.com/backpack/backpack/internal/node"
)

// `backpack node ...` — the managed side of the panel-to-server channel.
//
// There is almost nothing here, and that is the change.
//
// This used to hold a setup command, an agent, a service unit and a way to stop
// being managed, because the far server dialled the panel and had to be told
// how: a port to reach, a key to present, a daemon to hold the connection open.
// Every one of those was a thing to install on a machine the operator only
// wanted to use, and a thing that could be wrong while the panel showed the
// server as simply offline.
//
// The panel reaches servers over their own SSH now. That is already running,
// already authenticated and already how the machine is administered — so the
// far side needs no state at all, and what is left is the one command the panel
// runs there.

const nodeUsage = `backpack node — the panel-managed side of this server

  backpack node exec <request>
        Perform one operation and print the answer. Both are JSON, base64
        encoded. This is what a Backpack panel runs over SSH; there is no
        reason to type it.

Nothing needs to be set up here. A panel manages this server by logging in
over SSH, so adding it to a fleet is done entirely from the panel.
`

func runNode(args []string) {
	if len(args) == 0 {
		fmt.Print(nodeUsage)
		os.Exit(2)
	}
	switch args[0] {
	case "exec":
		nodeExec(args[1:])
	case "-h", "--help", "help":
		fmt.Print(nodeUsage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", args[0], nodeUsage)
		os.Exit(2)
	}
}

// nodeExec performs one operation for a panel reaching this server over SSH.
//
// The request arrives as an argument rather than on stdin because the panel
// gets to this through a shell, and a shell handed one opaque word has fewer
// ways to go wrong than one handed a redirect as well. Base64 for the same
// reason: nothing in it can be read as shell syntax, whatever the request holds.
//
// The answer always goes to stdout, including a refusal — the panel reads a
// Response either way, and a command that failed with nothing on stdout would
// reach it as "the far end said nothing", which is what a broken SSH looks like
// and is a different problem. The exit status stays 0 for the same reason; a
// non-zero one means this command failed, not that the operation did.
func nodeExec(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "node exec takes one base64 request")
		os.Exit(2)
	}
	raw, err := base64.StdEncoding.DecodeString(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "the request is not valid base64:", err)
		os.Exit(2)
	}
	var req node.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		fmt.Fprintln(os.Stderr, "the request is not valid JSON:", err)
		os.Exit(2)
	}
	out, err := json.Marshal(node.Execute(req))
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not encode the answer:", err)
		os.Exit(1)
	}
	fmt.Println(base64.StdEncoding.EncodeToString(out))
}
