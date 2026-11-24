// Package server is the Iran end of the plain reverse transports — tcp,
// tcpmux, ws, wss, wsmux and wssmux — ported from Backhaul's
// internal/server/transport (see package core for what that means).
//
// Backhaul kept each run's context, channels and control connection in fields
// that Restart replaced while the previous run's goroutines were still reading
// them. Here each run is a generation value those goroutines are handed, so a
// goroutine of a run that has ended sees that run's context end rather than the
// next run's fields — the same sequence of events, without the data race.
package server
