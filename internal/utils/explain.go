package utils

import (
	"regexp"
	"strings"

	"github.com/sirupsen/logrus"
)

// Warnings and errors in plain words, with what to do.
//
// The engines say what failed in an engineer's terms — "channel dialer: dial
// tcp 203.0.113.5:9000: connect: connection refused" — which is exact and
// tells an operator nothing about what to change. explainHook rewrites the
// lines it recognises into one plain sentence and the fix, and keeps the
// original at the end in brackets for anyone who needs it (support, a bug
// report). A line it does not recognise is left as it is.
//
// It works on the message rather than at each call site so the plain engine
// keeps the plain engine's code as it is, and every engine gets the same words for
// the same trouble.

// explainRule turns a matching message into its plain sentence. detail false
// leaves the original out — for a line that carries the tunnel token.
type explainRule struct {
	re     *regexp.Regexp
	say    func(m []string) string
	detail bool
}

func rule(pattern string, say func(m []string) string) explainRule {
	return explainRule{re: regexp.MustCompile(pattern), say: say, detail: true}
}

// portOf is the port of an address ("0.0.0.0:443", ":443", "[::]:443").
func portOf(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i+1:]
	}
	return addr
}

// explainRules are tried in order; the first match wins.
var explainRules = []explainRule{
	// A port this server cannot open.
	rule(`(?:listen(?:er)?|listening) on (\S+?):? .*address already in use`, func(m []string) string {
		return "Port " + portOf(m[1]) + " is already used by another program on this server. " +
			"Free it (see which: ss -lntup | grep :" + portOf(m[1]) + ") or choose another port."
	}),
	rule(`(?:listen(?:er)?|listening) on (\S+?):? .*permission denied`, func(m []string) string {
		return "Port " + portOf(m[1]) + " cannot be opened: ports below 1024 need root. " +
			"Run the tunnel as root or use a port above 1024."
	}),
	rule(`(?:listen(?:er)?|listening) on (\S+?):? .*cannot assign requested address`, func(m []string) string {
		return "This server has no address " + m[1] + " to listen on. Use 0.0.0.0, or an address this server really has."
	}),

	// The kharej reaching the service it forwards to.
	rule(`^local dialer: .*dial tcp (\S+): connect: connection refused`, func(m []string) string {
		return "Nothing is listening at " + m[1] + " on this (kharej) server — the service the tunnel forwards to is " +
			"not running, or listens on another port. Start it, or fix the port mapping on the Iran side."
	}),
	rule(`^local dialer: `, func([]string) string {
		return "The service this tunnel forwards to could not be reached on this (kharej) server. " +
			"Check that it is running and that the port mapping points at it."
	}),

	// The kharej reaching the Iran server.
	rule(`dialer: .*dial tcp (\S+): connect: connection refused`, func(m []string) string {
		return "The Iran server at " + m[1] + " refused the connection: nothing listens on that port there. " +
			"Check that the Iran tunnel is running and that the tunnel port is the same on both servers."
	}),
	rule(`dialer: .*dial tcp (\S+): (?:i/o timeout|connect: no route to host|connect: network is unreachable|connect: connection timed out)`, func(m []string) string {
		return "The Iran server at " + m[1] + " does not answer: the address is wrong, a firewall drops the tunnel port, " +
			"or the path is filtered. Check the address and open the port in the Iran server's firewall."
	}),
	rule(`(?:dialer: .*DNS resolution|no such host)`, func([]string) string {
		return "The Iran server's name could not be looked up. Check the domain, or use its IP address."
	}),

	// The token.
	{re: regexp.MustCompile(`^invalid security token received`), detail: false, say: func([]string) string {
		return "A client connected with a different token and was turned away. " +
			"If it was your kharej server, give both servers the same token."
	}},
	{re: regexp.MustCompile(`^invalid token received\.`), detail: false, say: func([]string) string {
		return "The Iran server answered with a different token. Give both servers the same token."
	}},

	// The other side going away.
	rule(`^control channel has been closed by the (client|server)`, func(m []string) string {
		other := "kharej"
		if m[1] == "server" {
			other = "Iran"
		}
		return "The " + other + " server closed the tunnel — it was stopped, restarted or updated. Reconnecting."
	}),
	rule(`^failed to read from (?:the )?(?:control )?channel`, func([]string) string {
		return "The connection to the other server dropped — it restarted, or the path cut out. " +
			"The tunnel reconnects by itself; if this repeats every few minutes, the path between the servers is unstable."
	}),
	rule(`^failed to send (?:heartbeat|request new connection|RTT)`, func([]string) string {
		return "Could not send to the other server: the connection dropped. Reconnecting."
	}),
	rule(`^timeout while waiting for control channel`, func([]string) string {
		return "The other server took too long to answer the handshake — a slow or lossy path, or another program " +
			"on the tunnel port. Retrying."
	}),

	// Two ends that do not speak alike.
	rule(`(?:unexpected response from (?:control )?channel|invalid signal received for channel)`, func([]string) string {
		return "The other server speaks differently: give both servers the same transport, and update both to the same version."
	}),
	rule(`failed to create (?:MUX|mux) session`, func([]string) string {
		return "A multiplexed session could not start. Give both servers the same transport and the same mux_version."
	}),

	// Load.
	rule(`(?:channel is full|channel with listener \S+ is full|is full, discarding|channel is full, cannot request)`, func([]string) string {
		return "More new connections arrived at once than the tunnel queues, and some were dropped. " +
			"Raise channel_size, or use the Aggressive preset."
	}),
	rule(`(?:UDP channel is full|is full, dropping udp packet)`, func([]string) string {
		return "UDP is arriving faster than the tunnel forwards it, and some packets were dropped."
	}),

	// The websocket port's visitors.
	rule(`^unauthorized request from (\S+)`, func(m []string) string {
		return "A connection to the websocket port from " + m[1] + " had no valid token — a scanner, or a kharej with " +
			"another token. Harmless unless it is your kharej."
	}),
	rule(`^failed to upgrade connection from (\S+)`, func(m []string) string {
		return "A connection to the websocket port from " + m[1] + " was not a websocket — a browser or a scanner. Harmless."
	}),

	// Settings.
	rule(`invalid (?:port range format|start port in range|end port in range|port format|port mapping format): (.*)`, func(m []string) string {
		return "The forwarded ports list has a mistake at \"" + m[1] + "\". Write ports like 443, 8080=80 or 2000-2010."
	}),
	rule(`(?:tls: failed to find any PEM|\.(?:pem|crt|key|cert): no such file|tls: private key does not match)`, func([]string) string {
		return "The TLS certificate or key could not be used. Check tls_cert and tls_key, or leave both empty " +
			"for an automatic certificate."
	}),
	rule(`failed to (?:dial remote UDP address|resolve remote address)`, func([]string) string {
		return "The UDP service this tunnel forwards to could not be reached on this (kharej) server. Check the port mapping."
	}),
}

// explainMarker starts the bracketed original, and marks a line already done.
const explainMarker = " [detail: "

// Explain rewrites msg into plain words with what to do, or returns it as it
// is when no rule knows it.
func Explain(msg string) string {
	if strings.Contains(msg, explainMarker) {
		return msg
	}
	for _, r := range explainRules {
		m := r.re.FindStringSubmatch(msg)
		if m == nil {
			continue
		}
		out := r.say(m)
		if r.detail {
			out += explainMarker + msg + "]"
		}
		return out
	}
	return msg
}

// explainHook applies Explain to warnings and errors as they are logged.
type explainHook struct{}

func (explainHook) Levels() []logrus.Level {
	return []logrus.Level{logrus.PanicLevel, logrus.FatalLevel, logrus.ErrorLevel, logrus.WarnLevel}
}

func (explainHook) Fire(e *logrus.Entry) error {
	e.Message = Explain(e.Message)
	return nil
}
