package config

// Spoof-carrier mode resolution, kept in one place so every consumer (the
// server, the client, and the startup validation) agrees on what "relay mode"
// means and where it forwards — and so the legacy spoof_pipe keys keep working
// without that alias logic being scattered across the dispatch sites.

// Relay mode — a bare datagram relay to a local UDP socket, instead of a
// tunnel — was a shape of the reverse spoof transport, and went with it. The
// direct tunnel carries a whole private network, which is what the relay was
// reached for: an inner transport that brings its own reliability (WireGuard,
// most often) is routed over the tunnel rather than piped through it.
