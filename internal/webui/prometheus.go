package webui

import (
	"time"

	"fmt"
	"github.com/backpack/backpack/internal/tunhist"
	"net/http"
	"strconv"
	"strings"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/sysstat"
)

// handlePrometheus serves the numbers in Prometheus text exposition format,
// for anyone running Grafana over several servers. It is raw values only —
// no strings, no formatting — because that is what a scraper wants.
//
// Reached with the remote access token (Settings → Remote access); a panel
// session works too, so it can be inspected from a browser.
func (s *server) handlePrometheus(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder

	// The panel's own numbers first: it is the component an operator reaches
	// for when something is wrong, and it was the one thing on the box with no
	// numbers at all. See selfmetrics.go.
	writePanelMetrics(&b)

	m := sysstat.Get()
	gauge(&b, "backpack_cpu_percent", "CPU usage percent", m.CPUPercent)
	gauge(&b, "backpack_mem_percent", "Memory usage percent", m.MemPercent)
	gauge(&b, "backpack_swap_percent", "Swap usage percent", m.SwapPercent)
	gauge(&b, "backpack_disk_percent", "Disk usage percent", m.DiskPercent)
	gauge(&b, "backpack_uptime_seconds", "System uptime in seconds", m.Uptime.Seconds())
	gauge(&b, "backpack_monitor_running", "1 when the backpack-monitor service is active",
		boolVal(manage.MonitorRunning()))

	tunnels := manage.List()
	health := manage.AllHealth()

	b.WriteString("# HELP backpack_tunnel_up 1 when the tunnel's peer is connected\n# TYPE backpack_tunnel_up gauge\n")
	for _, t := range tunnels {
		up := health[t.Name].State == "online"
		fmt.Fprintf(&b, "backpack_tunnel_up{name=%q,transport=%q,role=%q} %d\n",
			t.Name, t.Transport, t.Role, int(boolVal(up)))
	}

	counterHead(&b, "backpack_tunnel_bytes_in_total", "Bytes received over the tunnel")
	counterHead(&b, "backpack_tunnel_bytes_out_total", "Bytes sent over the tunnel")
	// What each tunnel's process is holding. These are the counters a leak
	// shows up in first — see internal/metrics/runtime.go — and the reason they
	// are here rather than left to a profiler is that nobody attaches a
	// profiler to a tunnel that is merely getting slowly worse.
	var runtimeSnaps []struct {
		name string
		rs   metrics.RuntimeStats
	}
	var kcpSnaps []metrics.Snapshot
	for _, t := range tunnels {
		snap, err := metrics.Read(app.ConfigDir, t.Name)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "backpack_tunnel_bytes_in_total{name=%q} %d\n", t.Name, snap.BytesIn)
		fmt.Fprintf(&b, "backpack_tunnel_bytes_out_total{name=%q} %d\n", t.Name, snap.BytesOut)
		if snap.Runtime != nil {
			runtimeSnaps = append(runtimeSnaps, struct {
				name string
				rs   metrics.RuntimeStats
			}{t.Name, *snap.Runtime})
		}
		if snap.KCP != nil {
			snap.Name = t.Name
			kcpSnaps = append(kcpSnaps, snap)
		}
	}

	// Uptime over the last week, per tunnel.
	//
	// The samples for it have been on disk since the history sampler existed —
	// UpN of N checks per hour — and nothing turned them into the figure an
	// operator is actually asked for. Both numbers are exported, not just the
	// percentage: 100% over twelve checks and 100% over two thousand are
	// different claims, and an alert built on the first is built on nothing.
	//
	// A tunnel nothing has sampled yet is absent rather than zero. Not measured
	// is not the same as down, and a young tunnel published at 3% is a number
	// somebody will page on.
	{
		var any bool
		for _, t := range tunnels {
			if _, _, ok := tunhist.UptimeOf(t.Name, 7*24*time.Hour); ok {
				any = true
				break
			}
		}
		if any {
			gaugeHead(&b, "backpack_tunnel_uptime_percent",
				"Percentage of health checks in the last 7 days that saw the tunnel up")
			gaugeHead(&b, "backpack_tunnel_uptime_checks",
				"How many health checks that percentage rests on")
			for _, t := range tunnels {
				pct, checks, ok := tunhist.UptimeOf(t.Name, 7*24*time.Hour)
				if !ok {
					continue
				}
				fmt.Fprintf(&b, "backpack_tunnel_uptime_percent{name=%q} %.4f\n", t.Name, pct)
				fmt.Fprintf(&b, "backpack_tunnel_uptime_checks{name=%q} %d\n", t.Name, checks)
			}
		}
	}

	if len(runtimeSnaps) > 0 {
		gaugeHead(&b, "backpack_tunnel_goroutines", "Goroutines held by the tunnel process")
		gaugeHead(&b, "backpack_tunnel_open_files", "File descriptors held by the tunnel process")
		gaugeHead(&b, "backpack_tunnel_heap_bytes", "Heap bytes in use by the tunnel process")
		gaugeHead(&b, "backpack_tunnel_heap_objects", "Live heap objects in the tunnel process")
		for _, r := range runtimeSnaps {
			fmt.Fprintf(&b, "backpack_tunnel_goroutines{name=%q} %d\n", r.name, r.rs.Goroutines)
			if r.rs.OpenFiles > 0 {
				fmt.Fprintf(&b, "backpack_tunnel_open_files{name=%q} %d\n", r.name, r.rs.OpenFiles)
			}
			fmt.Fprintf(&b, "backpack_tunnel_heap_bytes{name=%q} %d\n", r.name, r.rs.HeapBytes)
			fmt.Fprintf(&b, "backpack_tunnel_heap_objects{name=%q} %d\n", r.name, r.rs.HeapObjects)
		}
	}

	if len(kcpSnaps) > 0 {
		for _, c := range []struct {
			metric, help string
			val          func(*metrics.KCPStats) uint64
		}{
			{"backpack_kcp_retransmitted_total", "KCP segments sent again", func(k *metrics.KCPStats) uint64 { return k.Retransmitted }},
			{"backpack_kcp_lost_total", "KCP segments that never arrived", func(k *metrics.KCPStats) uint64 { return k.Lost }},
			{"backpack_kcp_fec_recovered_total", "Packets rebuilt by forward error correction", func(k *metrics.KCPStats) uint64 { return k.FECRecovered }},
		} {
			counterHead(&b, c.metric, c.help)
			for _, snap := range kcpSnaps {
				fmt.Fprintf(&b, "%s{name=%q} %d\n", c.metric, snap.Name, c.val(snap.KCP))
			}
		}
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Write([]byte(b.String()))
}

func gauge(b *strings.Builder, name, help string, v float64) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n",
		name, help, name, name, strconv.FormatFloat(v, 'f', -1, 64))
}

func counterHead(b *strings.Builder, name, help string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s counter\n", name, help, name)
}

// gaugeHead is counterHead for a value that goes down as well as up. Separate
// from gauge() because these carry a label per tunnel, so the header is written
// once and the samples follow.
func gaugeHead(b *strings.Builder, name, help string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
}

func boolVal(v bool) float64 {
	if v {
		return 1
	}
	return 0
}
