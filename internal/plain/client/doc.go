// Package client is the kharej end of the plain reverse transports — tcp,
// tcpmux, ws, wss, wsmux and wssmux (see package core
// for the wire they share).
//
// As on the server side, each run is a generation value handed to the
// goroutines that serve it, rather than replacing fields under them.
package client
