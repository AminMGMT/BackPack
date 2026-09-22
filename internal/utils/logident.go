package utils

import (
	"os"
	"sync/atomic"

	"github.com/sirupsen/logrus"
)

// Making the JSON logs worth shipping.
//
// JSON output has existed for a while and nobody could do much with it. Every
// line carried a timestamp, a level and a message and nothing else, so an
// operator with five servers who shipped all five journals to one place got a
// single stream in which no line said which machine or which tunnel it came
// from. Searching it meant knowing in advance which server to look at, which is
// the problem shipping was supposed to solve.
//
// Four fields fix that, and they are the four every query starts from: the
// tunnel, its role, its transport, and the host. They are attached to JSON
// entries only. The text format is read by a person running `journalctl` on the
// server they are already logged into, who knows all four already and would
// only have them repeated on every line.
//
// # Why this is process-wide state
//
// One process runs one tunnel. That is not an accident of the current code, it
// is the arrangement the whole product rests on — fault isolation comes from it
// and `docs/design-decisions.md` turns down the alternative by name. So the
// identity of "the tunnel this process is" is genuinely a property of the
// process, and threading it through every constructor would be modelling a
// variation that does not exist.
//
// It is set once, before the engine starts. A logger built before it is set
// carries no identity rather than a wrong one.

// LogIdentity is what a shipped log line needs in order to be findable.
type LogIdentity struct {
	// Tunnel is the tunnel's name, which is its configuration file's name
	// without the extension — the same name the CLI, the panel and the metrics
	// snapshot use.
	Tunnel string
	// Role is "server" or "client" for a reverse tunnel, or "l3"/"direct" for
	// the other two engines.
	Role string
	// Transport is the carrier in use, e.g. "wss" or "quic".
	Transport string
	// Host is this machine's hostname, so lines from two servers running a
	// tunnel of the same name can be told apart.
	Host string
}

var logIdentity atomic.Pointer[LogIdentity]

// SetLogIdentity records what this process is, for every JSON logger built
// afterwards. The hostname is filled in when it is left empty.
func SetLogIdentity(id LogIdentity) {
	if id.Host == "" {
		id.Host, _ = os.Hostname()
	}
	logIdentity.Store(&id)
}

// CurrentLogIdentity returns what was set, or the zero value.
func CurrentLogIdentity() LogIdentity {
	if id := logIdentity.Load(); id != nil {
		return *id
	}
	return LogIdentity{}
}

// logFieldNames are the JSON keys the identity is written under.
//
// They are a stable interface: a dashboard, an alert rule or a grep in
// somebody's runbook is written against these names, and renaming one silently
// breaks all of them on the next update. docs/log-schema.md documents them and
// a test pins them.
const (
	FieldTunnel    = "tunnel"
	FieldRole      = "role"
	FieldTransport = "transport"
	FieldHost      = "host"
)

// identityFormatter adds the identity to each entry before handing it to the
// formatter underneath.
type identityFormatter struct{ inner logrus.Formatter }

func (f identityFormatter) Format(e *logrus.Entry) ([]byte, error) {
	id := CurrentLogIdentity()
	if id == (LogIdentity{}) {
		return f.inner.Format(e)
	}

	// A copy, because the caller owns the entry and a field written into it
	// here would outlive this call if logrus ever reused one.
	data := make(logrus.Fields, len(e.Data)+4)
	for k, v := range e.Data {
		data[k] = v
	}
	set(data, FieldTunnel, id.Tunnel)
	set(data, FieldRole, id.Role)
	set(data, FieldTransport, id.Transport)
	set(data, FieldHost, id.Host)

	clone := *e
	clone.Data = data
	return f.inner.Format(&clone)
}

// set writes a field unless it is empty, or unless the caller already said
// something under that name — an explicit field on the call site is more
// specific than the process-wide default and wins.
func set(data logrus.Fields, key, value string) {
	if value == "" {
		return
	}
	if _, taken := data[key]; taken {
		return
	}
	data[key] = value
}
