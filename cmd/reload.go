package cmd

import (
	"context"
	"net"
	"os"
	"reflect"
	"time"

	"github.com/backpack/backpack/config"
)

// Picking up a changed configuration without being told to.
//
// Editing a tunnel's file used to do nothing at all until somebody restarted
// the service. That is a small thing when you remember, and a genuinely
// confusing one when you do not: the file on disk says one thing, the running
// tunnel does another, and nothing anywhere reports the disagreement. The
// panel's own editor works around it by restarting the unit itself, which only
// covers edits made through the panel — an ssh session and a text editor, which
// is how most of these files actually get changed, is left with a tunnel that
// quietly ignores what it was told.
//
// So the file is watched, and a change is applied by stopping the tunnel and
// starting it again with the new configuration, in the same process. What that
// buys over restarting the unit is not speed; it is that the operator does not
// have to know to do it.
//
// Two properties matter more than the feature does, and both are about not
// making things worse than the manual restart was:
//
//   - A file that does not parse is ignored, loudly. A half-saved file or a
//     typo must never take a working tunnel down — that would turn a mistake
//     the operator would have seen immediately into an outage they find out
//     about later.
//   - A file whose contents mean the same thing is ignored silently. Touching
//     a file, rewriting it identically, or editing a comment is not a reason
//     to drop every connection the tunnel is carrying.
//
// The file is polled rather than watched through the kernel. It costs one stat
// every couple of seconds, which is nothing next to what the tunnel is already
// doing, and it avoids a dependency; it also handles the way editors actually
// save — writing a new file and renaming it over the old one — which watching
// an inode does not, without extra care.

// configPollInterval is how often the file is checked. A few seconds is well
// inside "it just picked it up" for a human editing a file, and the check is a
// single stat.
const configPollInterval = 2 * time.Second

// portSettleTimeout bounds how long to wait for the stopped tunnel's ports to
// come free before starting the new one anyway.
const portSettleTimeout = 5 * time.Second

// fingerprint is a cheap identity for the file's current contents. Size and
// modification time together are enough to notice an edit; anything they miss
// is caught on the next edit, and a false positive costs only a parse and a
// comparison, which is why this does not need to hash the file.
type fingerprint struct {
	size    int64
	modTime time.Time
}

func fileFingerprint(path string) (fingerprint, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return fingerprint{}, err
	}
	return fingerprint{size: fi.Size(), modTime: fi.ModTime()}, nil
}

// awaitConfigChange blocks until the file at path holds a configuration that
// differs from current, and returns it. It returns nil when ctx ends, which is
// the caller's signal to shut down rather than reload.
//
// current must be the configuration as it was loaded, not the one the tunnel is
// running from: the transports write their status back into their own copy, so
// a running configuration always differs from the file it came from.
func awaitConfigChange(ctx context.Context, path string, current *config.Config) *config.Config {
	last, err := fileFingerprint(path)
	if err != nil {
		// The file was readable moments ago, when it was loaded. If it is not
		// now, wait for it to come back rather than treating that as a change.
		logger.Debugf("cannot stat the configuration file: %v", err)
	}
	// Remembering the last fingerprint that failed to parse keeps a broken file
	// from being reported once every poll for as long as it stays broken.
	var lastComplaint fingerprint

	ticker := time.NewTicker(configPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		fp, err := fileFingerprint(path)
		if err != nil || fp == last {
			continue
		}
		last = fp

		next, err := loadConfig(path)
		if err != nil {
			if fp != lastComplaint {
				lastComplaint = fp
				logger.Errorf("the configuration file changed but does not parse, so the tunnel keeps running the previous one: %v", err)
			}
			continue
		}
		applyDefaults(next)

		if reflect.DeepEqual(current, next) {
			logger.Debug("the configuration file changed but means the same thing; leaving the tunnel alone")
			continue
		}
		return next
	}
}

// waitForPorts waits until every address given can be bound, so the tunnel
// being started is not racing the one that just stopped for its own ports.
//
// The listeners close as soon as their context ends, but nothing reports when
// they have; the panel's HTTP server in particular is shut down gracefully and
// takes a moment. Binding the address is the only direct evidence that it is
// free, so that is what is checked — and if it never comes free, this gives up
// and lets the listener report the real error rather than hanging here.
func waitForPorts(ctx context.Context, addrs []string) {
	deadline := time.Now().Add(portSettleTimeout)
	for _, addr := range addrs {
		if addr == "" {
			continue
		}
		for time.Now().Before(deadline) {
			ln, err := net.Listen("tcp", addr)
			if err == nil {
				ln.Close()
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
}

// portsInUse names the addresses a run binds that can be known from the
// configuration alone — the tunnel's own listener and the per-tunnel web page.
//
// The forwarded ports are deliberately not included. Working them out means
// re-implementing the port-mapping parser, ranges and all, and they are the
// listeners that close immediately anyway; it is the gracefully shut down HTTP
// server that actually needs waiting for.
func portsInUse(cfg *config.Config) []string {
	var addrs []string
	if cfg.Server.BindAddr != "" {
		addrs = append(addrs, cfg.Server.BindAddr)
		if cfg.Server.WebPort > 0 {
			addrs = append(addrs, net.JoinHostPort("", itoa(cfg.Server.WebPort)))
		}
		return addrs
	}
	if cfg.Client.WebPort > 0 {
		addrs = append(addrs, net.JoinHostPort("", itoa(cfg.Client.WebPort)))
	}
	return addrs
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
