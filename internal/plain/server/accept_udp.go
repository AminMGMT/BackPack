package server

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/plain/core"
	"github.com/backpack/backpack/internal/web"
	"github.com/sirupsen/logrus"
)

const BufferSize = 16 * 1024

// udpTable is one forwarded UDP port's flows, by the address they come from.
type udpTable struct {
	mu sync.Mutex
	m  map[string]*LocalAcceptUDPConn
}

// release ends flow: its payload channel is closed and, unless a newer flow
// from the same address has taken its place (a congested one is replaced
// rather than removed), it leaves the table.
func (f *LocalAcceptUDPConn) release() {
	f.table.mu.Lock()
	defer f.table.mu.Unlock()
	close(f.payload)
	if f.table.m[f.clientAddr.String()] == f {
		delete(f.table.m, f.clientAddr.String())
	}
}

// port is the forwarded port the flow arrived on, for the usage sniffer.
func (f *LocalAcceptUDPConn) port() int { return f.listener.LocalAddr().(*net.UDPAddr).Port }

// listenUDP serves the UDP side of one forwarded port until ctx ends. Each new
// source address is a flow, handed to accept to be carried over a tunnel
// connection; accept reports false when it cannot be queued, and the flow is
// dropped. Every plain transport forwards UDP through it (Backpack: the plain
// engine did on tcp alone).
func listenUDP(ctx context.Context, holds *core.Holds, logger *logrus.Logger, localAddr, remoteAddr string, accept func(*LocalAcceptUDPConn) bool) {
	localUDPAddr, err := net.ResolveUDPAddr("udp", localAddr)
	if err != nil {
		logger.Fatalf("failed to resolve local address: %v", err)
	}

	listener, err := net.ListenUDP("udp", localUDPAddr)
	if err != nil {
		logger.Fatalf("failed to listen on local UDP port: %v", err)
	}

	defer holds.Hold()()
	defer listener.Close()

	logger.Infof("UDP listener started successfully, listening on address: %s", listener.LocalAddr().String())

	table := &udpTable{m: map[string]*LocalAcceptUDPConn{}}

	// Buffer for UDP reads
	buf := make([]byte, BufferSize-2) // 2 bytes reserved for header

	go func() {
		for {
			n, addr, err := listener.ReadFromUDP(buf)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				logger.Errorf("failed to read from UDP listener: %v", err)
				continue
			}

			// Create a unique identifier for the connection based on IP and port
			key := addr.String()

			table.mu.Lock()
			// Check if the connection is already active
			if existingConn, exists := table.m[key]; exists {
				if existingConn.IsCongested.Load() {
					s := "connection with timestamp %d congested. Removing %s from active connections due to network congestion"
					logger.Debugf(s, existingConn.timeCreated, addr.String())
					// For congested connections, closing the payload channel immediately can cause abrupt TCP disconnection,
					// potentially leading to data loss. Instead, allow the connection to keep transferring data for 30 more
					// seconds (or until the payload channel becomes idle). The timer will close the TCP connection once it
					// times out.
				} else {
					// If it exists, send the payload to the existing connection's payload channel
					select {
					case existingConn.payload <- append([]byte(nil), buf[:n]...): // Copy the packet to avoid data overwriting
						logger.Tracef("buffered %d bytes for existing connection %s", n, addr.String())
					default:
						logger.Warnf("payload channel for connection %s is full, dropping udp packet", addr.String())
					}
					table.mu.Unlock()
					continue
				}
			}

			// Create a new payload channel for this connection, Buffer up to 100,0000 packets for the connection
			// Generally affect the upload speed
			payloadChan := make(chan []byte, 100_000)
			payloadChan <- append([]byte(nil), buf[:n]...) // a copy of the first packet

			newUDPConn := &LocalAcceptUDPConn{
				timeCreated: time.Now().UnixNano(), // Just for debugging
				payload:     payloadChan,
				remoteAddr:  remoteAddr,
				listener:    listener,
				clientAddr:  addr,
				table:       table,
			}
			table.m[key] = newUDPConn
			table.mu.Unlock()

			if accept(newUDPConn) {
				logger.Debugf("accepted UDP connection from %s", addr.String())
				continue
			}
			// Backpack: a flow nothing will carry leaves the table, so the next
			// packet from it starts a new one rather than queueing behind it.
			logger.Warn("UDP channel is full, dropping packet.")
			table.mu.Lock()
			if table.m[key] == newUDPConn {
				delete(table.m, key)
			}
			table.mu.Unlock()
		}
	}()

	<-ctx.Done()
}

func (s *TcpTransport) udpListener(g *tcpGen, localAddr string, remoteAddr string) {
	// make a new channel for recieve udp packets
	udpChan := make(chan *LocalAcceptUDPConn, s.config.ChannelSize)

	// handle channel
	go s.handleUDPLoop(g, udpChan)

	listenUDP(g.ctx, &s.holds, s.logger, localAddr, remoteAddr, func(flow *LocalAcceptUDPConn) bool {
		select {
		case udpChan <- flow:
		default:
			return false
		}
		select {
		case g.reqNewConnChan <- struct{}{}: // Successfully requested a new tcp connection
		default: // The channel is full, do nothing
			s.logger.Warn("channel is full, cannot request a new connection")
		}
		return true
	})
}

func (s *TcpTransport) handleUDPLoop(g *tcpGen, udpChan chan *LocalAcceptUDPConn) {
	for {
		select {
		case <-g.ctx.Done():
			return
		case localConn := <-udpChan:
		loop:
			for {
				select {
				case <-g.ctx.Done():
					return

				case tunnelConn := <-g.tunnelChannel:
					// Send the target addr over the connection
					if err := core.SendBinaryTransportString(tunnelConn, localConn.remoteAddr, core.SG_UDP); err != nil {
						s.logger.Errorf("%v", err)
						tunnelConn.Close()
						continue loop
					}

					// Handle data exchange between connections
					go UDPConnectionHandler(g.ctx, localConn, core.Counted(tunnelConn), s.logger, g.usageMonitor, localConn.port(), s.config.Sniffer, g.rtt.Load())

					s.logger.Debugf("initiate new handler for connection %s with timestamp %d", localConn.clientAddr.String(), localConn.timeCreated)
					break loop
				}
			}
		}
	}
}

// noCongestion, as UDPConnectionHandler's rtt, turns off moving a flow that
// falls behind onto a new tunnel connection. A mux stream that falls behind
// would move to another stream of the same sessions, which helps nothing; and
// the check compares the kharej's clock with this server's, which only works
// where the two agree.
const noCongestion = -1

// UDPConnectionHandler carries one UDP flow over tcp, a tunnel connection or
// stream, until either side ends or the flow is idle for a minute.
func UDPConnectionHandler(ctx context.Context, udp *LocalAcceptUDPConn, tcp net.Conn, logger *logrus.Logger, usage *web.Usage, remotePort int, sniffer bool, rtt int64) {
	done := make(chan struct{})
	// Backpack: the tunnel ending ends the flow, rather than a minute later.
	stop := context.AfterFunc(ctx, func() { tcp.Close() })
	defer stop()

	if rtt == 0 {
		// RTT of 0 indicates that either the tunnel is running in a local environment
		// (with negligible latency), or RTT measurement failed.
		// Set a default RTT of 100ms to ensure proper functioning of TCP congestion control.
		rtt = 100
	}

	go func() {
		udpToTCP(tcp, udp, logger, usage, remotePort, sniffer)
		tcp.Close()
		done <- struct{}{}
	}()

	tcpToUDP(tcp, udp, logger, usage, remotePort, sniffer, rtt)
	tcp.Close()

	<-done

	udp.release()
}

func udpToTCP(tcp net.Conn, udp *LocalAcceptUDPConn, logger *logrus.Logger, usage *web.Usage, remotePort int, sniffer bool) {
	// Create a header (2 bytes) to hold the size of the data
	header := make([]byte, 2)

	inactivityTimeout := 60 * time.Second // Define a 60-second inactivity timeout

	for {
		select {
		case data, ok := <-udp.payload: // Wait for data on the UDP payload channel
			if !ok {
				return
			}

			packetSize := len(data) // Calculate the packet size (data length)

			// the listener buffer size is 16KB, just for preventing bugs in the future!
			if packetSize > 65535 { // Check for overflow, since 2 bytes can only store values up to 65535 ~ 64KB
				logger.Errorf("packet too large to send, size: %d bytes", packetSize)
				continue
			}

			binary.BigEndian.PutUint16(header, uint16(packetSize)) // Store the packet size at 2 bytes

			// Prepend the header to the data
			packet := append(header, data...)

			totalWritten := 0
			for totalWritten < len(packet) { // Use the total packet length (header + data)
				w, err := tcp.Write(packet[totalWritten:])
				if err != nil {
					logger.Errorf("failed to write UDP payload to TCP: %v", err)
					return
				}
				totalWritten += w
			}

			logger.Tracef("received %d bytes, forwarded %d bytes from UDP to TCP", packetSize, totalWritten-2)

			if sniffer {
				usage.AddOrUpdatePort(remotePort, uint64(totalWritten))
			}

		case <-time.After(inactivityTimeout): // Timeout after 30 seconds of inactivity
			logger.Debugf("connection with timestamp %d and address %s idle for 60 seconds, closing", udp.timeCreated, udp.clientAddr.String())
			return
		}
	}
}

func tcpToUDP(tcp net.Conn, udp *LocalAcceptUDPConn, logger *logrus.Logger, usage *web.Usage, remotePort int, sniffer bool, rtt int64) {
	buf := make([]byte, BufferSize)
	lenBuf := make([]byte, 2)       // Buffer to store the 2-byte packet length
	timestampBuf := make([]byte, 4) // Buffer for timestamp (4 bytes)

	for {
		// First, read the 4-byte timestamp from the packet
		_, err := io.ReadFull(tcp, timestampBuf)
		if err != nil {
			if err == io.EOF {
				logger.Debug("TCP connection closed.")
			} else {
				logger.Debugf("failed to read timestamp from TCP connection: %v", err)
			}
			return
		}

		// 4-byte timestamp header
		packetTimestamp := int64(binary.BigEndian.Uint32(timestampBuf))

		// Get the current time and calculate the time difference
		timestamp := time.Now().UnixMilli()
		lastMillis := timestamp % (10 * 60 * 1000)

		packetAge := lastMillis - packetTimestamp

		// If the packet age exceeds the threshold (3x RTT), flag the connection as congested
		if rtt > 0 && packetAge > 3*rtt {
			udp.IsCongested.Store(true)
		}

		// Read the 2-byte packet length header from the TCP connection
		_, err = io.ReadFull(tcp, lenBuf)
		if err != nil {
			if err == io.EOF {
				logger.Debug("TCP connection closed.")
			} else {
				logger.Errorf("failed to read packet length from TCP connection: %v", err)
			}
			return
		}

		// Convert the 2-byte length header into an integer
		packetSize := int(binary.BigEndian.Uint16(lenBuf))

		// Check if the packet size is valid
		if packetSize > len(buf) {
			logger.Errorf("packet size exceeds buffer size: %d bytes", packetSize)
			return
		}

		// Now use io.ReadFull to read the actual packet data from TCP based on the packetSize
		_, err = io.ReadFull(tcp, buf[:packetSize])
		if err != nil {
			if err == io.EOF {
				logger.Debug("TCP connection closed.")
			} else {
				logger.Errorf("failed to read from TCP connection: %v", err)
			}
			return
		}

		// Forward the data to the UDP client address
		if udp.clientAddr != nil {
			totalWritten := 0
			for totalWritten < packetSize {
				w, err := udp.listener.WriteToUDP(buf[totalWritten:packetSize], udp.clientAddr)
				if err != nil {
					logger.Errorf("failed to forward TCP response to UDP client: %v", err)
					return
				}

				totalWritten += w
			}

			if sniffer {
				usage.AddOrUpdatePort(remotePort, uint64(totalWritten))
			}

			logger.Tracef("read %d bytes from TCP, forwarded %d bytes to UDP", packetSize, totalWritten)
		}
	}
}
