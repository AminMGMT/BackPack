package manage

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/backpack/backpack/internal/app"
)

// Game latency testing.
//
// The Link Test measures the tunnel itself — the leg between the two servers,
// which is the part an operator controls. This measures the leg past it: from
// the abroad (kharej) exit out to a game server, which is the part that decides
// whether the whole path is playable. Neither number is the full story on its
// own; run on the exit, this one answers "once traffic reaches abroad, how far
// is it to the game?", and the player's own latency to the hub is added on top.
//
// It pings over ICMP rather than opening a TCP connection the way ProbePath
// does, because a game endpoint is a third party we do not control: many answer
// ICMP but expose no TCP port to probe, and the ones that filter ICMP are
// reported as "no reply" rather than guessed at.

// GameEndpoint is one game server (or its datacentre edge) the test can probe.
type GameEndpoint struct {
	Game   string
	Region string
	Host   string
	Note   string
}

// gameListFile is the editable endpoint list. The defaults are written here on
// first use; publishers move addresses without notice, so an operator can
// correct the list with hosts they have verified themselves.
var gameListFile = app.ConfigDir + "/game-endpoints.list"

// defaultGameEndpoints is the bundled, best-effort list. The addresses are the
// public edges of each publisher's nearest region; they are a starting point,
// not gospel, which is why the list is editable.
func defaultGameEndpoints() []GameEndpoint {
	return []GameEndpoint{
		{"Dota 2", "Europe West", "185.25.183.1", "Valve Luxembourg"},
		{"Dota 2", "Europe East", "155.133.238.1", "Valve Vienna"},
		{"Dota 2", "Dubai", "185.25.180.1", "Valve Dubai"},
		{"CS2 / CSGO", "Europe West", "155.133.240.1", "Valve Frankfurt"},
		{"CS2 / CSGO", "Europe East", "155.133.238.1", "Valve Vienna"},
		{"CS2 / CSGO", "Dubai", "185.25.180.1", "Valve Dubai"},
		{"Valorant", "Europe", "104.18.32.1", "Riot EU edge"},
		{"League of Legends", "Europe West", "162.249.72.1", "Riot EUW"},
		{"PUBG", "Europe", "52.58.0.1", "AWS Frankfurt"},
		{"PUBG", "Middle East", "157.241.0.1", "AWS Bahrain"},
		{"Rainbow Six Siege", "Europe", "185.38.0.1", "Ubisoft EU"},
		{"Call of Duty", "Europe", "52.58.0.1", "Activision EU edge"},
		{"eFootball", "Europe", "35.156.0.1", "Konami EU"},
		{"Fortnite", "Europe", "18.194.0.1", "Epic EU"},
		{"Apex Legends", "Europe", "162.254.192.1", "EA EU"},
		{"Rocket League", "Europe", "35.156.0.1", "Psyonix EU"},
		{"Minecraft (Hypixel)", "Europe", "172.65.230.1", "Hypixel EU"},
		{"GTA Online", "Europe", "192.81.241.1", "Rockstar EU"},
	}
}

// LoadGameEndpoints reads the editable list, seeding it from the defaults the
// first time. A malformed or missing file falls back to the built-in list so
// the test always has something to run against.
func LoadGameEndpoints() []GameEndpoint {
	data, err := os.ReadFile(gameListFile)
	if err != nil {
		_ = saveGameEndpoints(defaultGameEndpoints())
		return defaultGameEndpoints()
	}
	eps := parseGameEndpoints(string(data))
	if len(eps) == 0 {
		return defaultGameEndpoints()
	}
	return eps
}

// parseGameEndpoints reads "game|region|host|note" lines, ignoring blanks and
// '#' comments.
func parseGameEndpoints(text string) []GameEndpoint {
	var out []GameEndpoint
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "|")
		if len(f) < 3 {
			continue
		}
		ep := GameEndpoint{
			Game:   strings.TrimSpace(f[0]),
			Region: strings.TrimSpace(f[1]),
			Host:   strings.TrimSpace(f[2]),
		}
		if len(f) >= 4 {
			ep.Note = strings.TrimSpace(f[3])
		}
		if ep.Game == "" || ep.Host == "" {
			continue
		}
		out = append(out, ep)
	}
	return out
}

// saveGameEndpoints writes the list back in the same format.
func saveGameEndpoints(eps []GameEndpoint) error {
	if err := os.MkdirAll(app.ConfigDir, 0755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# Game latency endpoints — one per line: game|region|host|note\n")
	b.WriteString("# Addresses are best-effort; replace them with hosts you have verified.\n")
	for _, e := range eps {
		fmt.Fprintf(&b, "%s|%s|%s|%s\n", e.Game, e.Region, e.Host, e.Note)
	}
	return os.WriteFile(gameListFile, []byte(b.String()), 0644)
}

// GamePing is the result of pinging one host: average round trip and loss.
type GamePing struct {
	AvgMS   float64
	LossPct float64
	OK      bool // false when the host returned no ICMP reply at all
}

var (
	rePingRTT  = regexp.MustCompile(`=\s*[0-9.]+/([0-9.]+)/`)
	rePingLoss = regexp.MustCompile(`([0-9.]+)% packet loss`)
)

// parsePing pulls the average RTT and loss out of the `ping` summary. It is
// separated from the exec so the parsing can be tested without a network.
func parsePing(out string) GamePing {
	var g GamePing
	if m := rePingLoss.FindStringSubmatch(out); m != nil {
		g.LossPct, _ = strconv.ParseFloat(m[1], 64)
	} else {
		g.LossPct = 100
	}
	if m := rePingRTT.FindStringSubmatch(out); m != nil {
		g.AvgMS, _ = strconv.ParseFloat(m[1], 64)
		g.OK = true
	}
	return g
}

// pingHost runs ICMP ping to a host and returns the parsed result. count probes
// are sent 0.2s apart with a 2s per-probe timeout, matching the Link Test's feel.
func pingHost(host string, count int) GamePing {
	cmd := exec.Command("ping", "-c", strconv.Itoa(count), "-i", "0.2", "-W", "2", "-q", host)
	out, _ := cmd.CombinedOutput() // ping exits non-zero on 100% loss; parse anyway
	return parsePing(string(out))
}

// GameRating is a verdict word for an estimated ping, with a severity the menu
// uses to colour it: 0 good, 1 marginal, 2 bad.
type GameRating struct {
	Label    string
	Severity int
}

// RateEstimatedPing turns a final estimated ping in milliseconds into a verdict.
// The thresholds are the ones a competitive player feels: under 60 ms is
// unremarkable, 130 ms is the edge of playable, past 180 ms it fights back.
func RateEstimatedPing(ms int) GameRating {
	switch {
	case ms < 60:
		return GameRating{"excellent", 0}
	case ms < 90:
		return GameRating{"good", 0}
	case ms < 130:
		return GameRating{"playable", 1}
	case ms < 180:
		return GameRating{"rough", 1}
	default:
		return GameRating{"bad", 2}
	}
}

// playerLatencies are the typical last-mile additions from a player to the Iran
// hub — the leg the server cannot measure — so an exit-side number can be turned
// into an honest estimate of what a real player will feel.
var playerLatencies = []struct {
	Where string
	MS    int
}{
	{"Fibre", 10},
	{"ADSL", 25},
	{"Mobile 4G", 45},
	{"Remote province", 60},
}
