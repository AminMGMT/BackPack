package manage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

const connTestResolveWait = 5 * time.Second

var connTestPathCheckWait = 12 * time.Second

// ConnTestPath keeps measurements and failures attached to the address actually
// dialled. An unreachable coordinator is not a verdict about tunnel protocols.
type ConnTestPath struct {
	Host    string           `json:"host"`
	Family  string           `json:"family"`
	State   string           `json:"state"`
	Error   string           `json:"error,omitempty"`
	Kharej  string           `json:"kharej,omitempty"`
	Rows    []ConnTestResult `json:"rows,omitempty"`
	Best    ConnTestBest     `json:"best"`
	Started int64            `json:"started,omitempty"`
}

// ConnTestSuite owns one or two independent tests. Only the selected path's
// peer engines and measurements run at a time; no speed run competes with the
// other family. Coordinators stay available to report a failed route through
// a working one, including when the first selected route is unreachable.
type ConnTestSuite struct {
	mu      sync.Mutex
	paths   []ConnTestPath
	tests   []*ConnTestIran
	changed chan struct{}
}

func (s *ConnTestSuite) Paths() []ConnTestPath {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]ConnTestPath(nil), s.paths...)
	for i := range out {
		out[i].Rows = append([]ConnTestResult(nil), out[i].Rows...)
	}
	return out
}

func (s *ConnTestSuite) Close() {
	for _, test := range s.tests {
		if test != nil {
			test.Close()
		}
	}
}

// Two-path links are a versioned envelope of ordinary test endpoints. Single
// paths still use v1 links, and old v1 links continue to work. No tunnel wire
// format changes. Both ends need this version to read a two-path envelope.
func ctSuiteLink(routes []connTestAddr) string {
	raw, _ := json.Marshal(routes)
	return connTestScheme + base64.RawURLEncoding.EncodeToString(append([]byte{2}, raw...))
}

func ctParseSuiteLink(raw []byte) (connTestAddr, error) {
	var routes []connTestAddr
	if len(raw) > 2048 || json.Unmarshal(raw, &routes) != nil || len(routes) != 2 {
		return connTestAddr{}, errors.New("the two-path test link is damaged — copy it again")
	}
	seen := map[string]bool{}
	first := -1
	for i, r := range routes {
		ip := net.ParseIP(r.Host)
		if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || seen[ctFamily(r.Host)] || len(r.Error) > 256 {
			return connTestAddr{}, errors.New("the test link must contain one IPv4 and one IPv6 address")
		}
		seen[ctFamily(r.Host)] = true
		if r.Error != "" && r.Coord == 0 && r.Tok == "" {
			continue
		}
		secret, err := base64.RawURLEncoding.DecodeString(r.Tok)
		if err != nil || len(secret) != connTestSecret || r.Coord < 1 || r.Coord > 65535 || r.Error != "" {
			return connTestAddr{}, errors.New("the test link contains a damaged coordinator endpoint")
		}
		if first < 0 {
			first = i
		}
	}
	if first < 0 {
		return connTestAddr{}, errors.New("the test link has no available path")
	}
	a := routes[first]
	a.Routes = routes
	return a, nil
}

func StartConnTestSuite(o ConnTestOptions, hosts []string) (*ConnTestSuite, string, error) {
	if len(hosts) < 1 || len(hosts) > 2 {
		return nil, "", errors.New("select one address, or one IPv4 and one IPv6 address")
	}
	if len(hosts) == 2 && ctFamily(hosts[0]) == ctFamily(hosts[1]) {
		return nil, "", errors.New("a two-path test requires one IPv4 and one IPv6 address")
	}
	s := &ConnTestSuite{changed: make(chan struct{}, 1), tests: make([]*ConnTestIran, len(hosts))}
	routes := make([]connTestAddr, len(hosts))
	until := time.Now().Add(connTestJoinWait + time.Duration(len(hosts))*ConnTestRunTime() + connTestSlack).Unix()
	available := 0
	o.ports = make(map[int]bool)
	for i, host := range hosts {
		o.Host = host
		test, _, err := StartConnTestIran(o)
		p := ConnTestPath{Host: host, Family: ctFamily(host), State: "waiting"}
		routes[i].Host = host
		if err != nil {
			p.State, p.Error = "unavailable", "could not prepare this path: "+ctShortErr(err)
			routes[i].Error = p.Error
		} else {
			s.tests[i] = test
			test.link.Until = until
			test.coord.setConfig(ctConfig(test.link))
			routes[i] = connTestAddr{Host: test.link.Host, Coord: test.link.Coord, Tok: test.link.Tok}
			available++
		}
		s.paths = append(s.paths, p)
		if o.Context != nil && o.Context.Err() != nil {
			s.Close()
			return nil, "", o.Context.Err()
		}
	}
	if available == 0 {
		s.Close()
		return nil, "", errors.New(s.paths[0].Error)
	}
	for _, test := range s.tests {
		if test != nil {
			test.coord.mu.Lock()
			test.coord.routeUnavailable = s.unavailable
			test.coord.mu.Unlock()
		}
	}
	if len(hosts) == 1 {
		return s, s.tests[0].link.Short(), nil
	}
	return s, ctSuiteLink(routes), nil
}

func (s *ConnTestSuite) unavailable(i int) bool {
	return s.markUnavailable(i, "Kharej could not reach this path's test coordinator over TCP/UDP; tunnel protocols were not measured")
}

func (s *ConnTestSuite) markUnavailable(i int, reason string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 || i >= len(s.paths) || s.paths[i].State == "running" || s.paths[i].State == "done" {
		return false
	}
	if s.paths[i].State == "unavailable" {
		return true
	}
	s.paths[i].State = "unavailable"
	s.paths[i].Error = reason
	select {
	case s.changed <- struct{}{}:
	default:
	}
	return true
}

func (s *ConnTestSuite) Run(ctx context.Context, progress func([]ConnTestPath)) []ConnTestPath {
	var reportMu sync.Mutex
	report := func() {
		reportMu.Lock()
		defer reportMu.Unlock()
		if progress != nil {
			progress(s.Paths())
		}
	}
	report()
	joinDeadline := time.Now().Add(connTestJoinWait)
	for i, test := range s.tests {
		if test == nil {
			continue
		}
		wait := time.NewTimer(time.Until(joinDeadline))
		waiting := true
		for waiting && ctx.Err() == nil {
			if s.Paths()[i].State == "unavailable" {
				break
			}
			select {
			case <-test.Joined():
				waiting = false
			case <-s.changed:
				report()
			case <-ctx.Done():
			case <-wait.C:
				s.markUnavailable(i, "Kharej did not join this path before the deadline; tunnel protocols were not measured")
				report()
			}
		}
		wait.Stop()
		if ctx.Err() != nil {
			break
		}
		s.mu.Lock()
		if s.paths[i].State == "unavailable" {
			s.mu.Unlock()
			continue
		}
		s.paths[i].State, s.paths[i].Kharej, s.paths[i].Started = "running", test.Kharej(), time.Now().Unix()
		s.mu.Unlock()
		report()
		rows := test.Run(ctx, func(row int, r ConnTestResult) {
			s.mu.Lock()
			for len(s.paths[i].Rows) <= row {
				s.paths[i].Rows = append(s.paths[i].Rows, ConnTestResult{})
			}
			s.paths[i].Rows[row] = r
			s.mu.Unlock()
			report()
		})
		s.mu.Lock()
		s.paths[i].Rows, s.paths[i].Best = rows, test.Best()
		s.paths[i].State = "done"
		if ctx.Err() != nil {
			s.paths[i].State = "stopped"
			s.paths[i].Best = ConnTestBest{}
		}
		s.mu.Unlock()
		report()
		// Wait until the peer has its result and has stopped its measurement,
		// before allowing the next path to start. Cancellation interrupts this.
		select {
		case <-test.Fetched():
		case <-ctx.Done():
		case <-time.After(30 * time.Second):
		}
		if ctx.Err() == nil {
			test.Close()
		}
		joinDeadline = time.Now().Add(connTestJoinWait)
	}
	if ctx.Err() != nil {
		s.stopPeers()
		s.mu.Lock()
		for i := range s.paths {
			if s.paths[i].State == "waiting" || s.paths[i].State == "running" {
				s.paths[i].State = "stopped"
			}
		}
		s.mu.Unlock()
		report()
	}
	return s.Paths()
}

// Give a reachable peer one poll to hear the cancellation before closing its
// coordinator. This context deliberately outlives the cancelled measurement,
// but the grace is shared by all paths and never exceeds three seconds.
func (s *ConnTestSuite) stopPeers() {
	var waiting []<-chan struct{}
	for _, test := range s.tests {
		if test == nil {
			continue
		}
		test.coord.mu.Lock()
		test.coord.stopped = true
		test.coord.mu.Unlock()
		waiting = append(waiting, test.Fetched())
	}
	grace := time.NewTimer(3 * time.Second)
	defer grace.Stop()
	for _, ack := range waiting {
		select {
		case <-ack:
		case <-grace.C:
			return
		}
	}
}

// ConnTestSuiteBest compares only paths whose tunnels passed the traffic
// checks. A route with a fast handshake and failed transfers cannot win.
func ConnTestSuiteBest(paths []ConnTestPath) ConnTestBest {
	var best ConnTestBest
	for _, p := range paths {
		b := p.Best
		if p.State != "done" || b.Transport == "" {
			continue
		}
		if best.Transport == "" || b.Mbps > best.Mbps || (b.Mbps == best.Mbps && b.RTTms < best.RTTms) {
			best = b
		}
	}
	return best
}

func runConnTestSuiteKharej(ctx context.Context, routes []connTestAddr, out io.Writer, progress func(int, ConnTestPath)) ([]ConnTestPath, error) {
	paths := make([]ConnTestPath, len(routes))
	links := make([]ConnTestLink, len(routes))
	report := func(i int) {
		if progress != nil {
			progress(i, paths[i])
		}
	}
	// Check every route before joining the first one. If IPv4 is unreachable,
	// use IPv6 to tell Iran to advance immediately, rather than waiting 15 min.
	for i, route := range routes {
		paths[i] = ConnTestPath{Host: route.Host, Family: ctFamily(route.Host), State: "waiting"}
		fmt.Fprintf(out, "Checking %s path %s...\n", paths[i].Family, route.Host)
		var err error
		if route.Error != "" {
			err = errors.New(route.Error)
		} else {
			check, cancel := context.WithTimeout(ctx, connTestPathCheckWait)
			links[i], err = fetchConnTestLink(check, route)
			cancel()
		}
		if err != nil {
			if errors.Is(err, errConnTestStopped) || ctx.Err() != nil {
				paths[i].State, paths[i].Error = "stopped", err.Error()
				report(i)
				return paths, err
			}
			paths[i].State, paths[i].Error = "unavailable", err.Error()
			fmt.Fprintln(out, paths[i].Error)
		}
		report(i)
		if ctx.Err() != nil {
			return paths, ctx.Err()
		}
	}
	// Best effort, through all reachable coordinators. Requests are authenticated
	// with that coordinator's secret and only accept an existing waiting path.
	notify := func(index int) {
		for j, route := range routes {
			if links[j].Host == "" || j == index || paths[j].State == "done" {
				continue
			}
			for attempt := 0; attempt < 2 && ctx.Err() == nil; attempt++ {
				reply, err := ctAskContext(ctx, route.Host, route.Coord, "unavailable "+route.Tok+" "+strconv.Itoa(index))
				if err == nil && reply == "ok" {
					return
				}
			}
		}
	}
	available := 0
	for i := range paths {
		if paths[i].State == "unavailable" {
			notify(i)
		} else {
			available++
		}
	}
	if available == 0 {
		return paths, errors.New("neither selected path's coordinator answered; tunnel protocols were not measured")
	}
	completed := 0
	for i := range paths {
		if paths[i].State == "unavailable" {
			continue
		}
		// The previous worker has finished and removed its engines. Recheck
		// cancellation before creating any engines for a later path; its
		// initial preflight may now be several minutes old.
		if i > 0 {
			check, cancel := context.WithTimeout(ctx, connTestPathCheckWait)
			refreshed, err := fetchConnTestLink(check, routes[i])
			cancel()
			if err != nil {
				paths[i].State, paths[i].Error = "unavailable", err.Error()
				if errors.Is(err, errConnTestStopped) || ctx.Err() != nil {
					paths[i].State = "stopped"
					report(i)
					return paths, err
				}
				notify(i)
				report(i)
				continue
			}
			links[i] = refreshed
		}
		paths[i].State = "running"
		report(i)
		fmt.Fprintf(out, "\nTesting %s · %s\n", paths[i].Family, paths[i].Host)
		// Bound a failed result channel to this path, not the lifetime of
		// the whole suite, so a later family still gets its turn.
		pathCtx, cancelPath := context.WithTimeout(ctx, ConnTestRunTime()+connTestSlack)
		rows, best, err := runConnTestKharejLink(pathCtx, links[i], out, func(rows []ConnTestResult) { paths[i].Rows = rows; report(i) })
		cancelPath()
		paths[i].Rows, paths[i].Best = rows, best
		paths[i].State = "done"
		if err != nil {
			if errors.Is(err, errConnTestStopped) || ctx.Err() != nil {
				paths[i].State, paths[i].Error = "stopped", err.Error()
				report(i)
				return paths, err
			}
			paths[i].State, paths[i].Error = "unavailable", err.Error()
			notify(i)
		} else {
			completed++
		}
		report(i)
		if ctx.Err() != nil {
			return paths, ctx.Err()
		}
	}
	if completed == 0 {
		return paths, errors.New("neither selected path completed its test; inspect each path's error")
	}
	return paths, nil
}
