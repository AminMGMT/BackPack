// Package server is the Iran end of the plain reverse transports — tcp,
// tcpmux, ws, wss, wsmux and wssmux (see package core
// for the wire they share).
//
// Each run's context, channels and control connection are not fields that a
// Restart replaces while the previous run's goroutines may still read them. Here each run is a generation value those goroutines are handed, so a
// goroutine of a run that has ended sees that run's context end rather than the
// next run's fields — the same sequence of events, without the data race.
package server
