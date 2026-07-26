package network

// TCP socket tuning that applies to every TCP-based transport.

// PinTCPBuffers decides whether TCP sockets get a fixed SO_RCVBUF/SO_SNDBUF.
//
// It defaults to false, and that default is the point. Calling setsockopt for
// either buffer makes Linux mark the socket buffer-locked and stop auto-tuning
// it — the kernel will no longer grow the window to fit the path. On a link
// with real bandwidth and the usual Iran-to-abroad latency, the window the
// kernel would have chosen is far larger than any fixed value we could pick,
// so pinning a "large" 8 MB buffer actively caps throughput. Measured on a
// fast uplink, unpinning is worth several times the speed.
//
// It is worse than it looks, because Optimize raises net.ipv4.tcp_rmem's
// ceiling to 256 MB specifically so autotuning has room — and then pinning
// switched off the thing that would have used it.
//
// The datagram transports are unaffected: a UDP socket has no equivalent
// autotuning, and a big receive buffer there genuinely prevents drops under
// burst, so KCP keeps setting its own buffers on its own socket.
//
// Set true (config: so_pin_tcp) only to reproduce the old behaviour.
var PinTCPBuffers = false

// TCPCongestion is the congestion control algorithm requested per socket.
// BBR keeps the pipe full on a long, mildly-lossy path where the loss-based
// default (cubic) reads loss as congestion and backs off. Applied best effort:
// a kernel without it simply keeps its default.
var TCPCongestion = "bbr"
