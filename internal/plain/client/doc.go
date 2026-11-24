// Package client is the kharej end of the plain reverse transports — tcp,
// tcpmux, ws, wss, wsmux and wssmux — ported from Backhaul's
// internal/client/transport (see package core for what that means).
//
// As on the server side, each run is a generation value handed to the
// goroutines that serve it, where Backhaul replaced the fields under them.
package client
