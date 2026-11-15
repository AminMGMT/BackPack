package naive

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/backpack/backpack/config"
)

// Every pooled reverse connection used to be a stream of one HTTP/2
// connection, whose flow-control window per round trip was the tunnel's speed
// (22 Mbit/s up through the tunnel on a 70 ms path). The client now spreads
// them over several; unset means the default, and 1 keeps a single one.
func TestTheNaiveClientSpreadsOverSeveralConnections(t *testing.T) {
	for _, c := range []struct {
		set  int
		want string
	}{{0, "4"}, {1, "1"}, {8, "8"}} {
		body, err := clientJSON(config.NaiveClientConfig{Server: "tunnel.example.com:443", Username: "u", Password: "p", Connections: c.set}, "127.0.0.1:1080")
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatal(err)
		}
		if got := m["insecure-concurrency"]; got != c.want {
			t.Errorf("connections = %d: insecure-concurrency %v, want %q", c.set, got, c.want)
		}
	}
}

func TestNaiveConnectionsOutOfRangeAreRefused(t *testing.T) {
	for _, n := range []int{-1, 17} {
		c := &config.ClientConfig{Transport: config.TCP, RemoteAddr: "127.0.0.1:3080",
			Naive: config.NaiveClientConfig{Binary: "/bin/true", Server: "tunnel.example.com:443", Username: "u", Password: "p", Connections: n}}
		if err := ValidateClient(c); err == nil || !strings.Contains(err.Error(), "connections") {
			t.Errorf("connections = %d: %v, want the range refused", n, err)
		}
	}
}
