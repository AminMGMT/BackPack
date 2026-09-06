package utils

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

func SendBinaryString(conn interface{}, message string) error {
	// Header size
	const headerSize = 2

	// Create a buffer with the appropriate size for the message
	buf := make([]byte, headerSize+len(message))

	// Encode the length of the message as a big-endian 2-byte unsigned integer
	binary.BigEndian.PutUint16(buf[:headerSize], uint16(len(message)))

	// Copy the message into the buffer after the length
	copy(buf[headerSize:], message)

	switch c := conn.(type) {
	case net.Conn:
		// Send the buffer over the connection
		if _, err := c.Write(buf); err != nil {
			return fmt.Errorf("failed to send message: %w", err)
		}

	default:
		// Handle unsupported connection types
		return fmt.Errorf("unsupported connection type: %T", conn)
	}
	// Successful
	return nil
}

func ReceiveBinaryString(conn interface{}) (string, error) {
	// Header size
	const headerSize = 2

	// Create a buffer to read the first 2 bytes (the length of the message)
	lenBuf := make([]byte, headerSize)

	switch c := conn.(type) {
	case net.Conn:
		// Read exactly 2 bytes for the message length
		if _, err := io.ReadFull(c, lenBuf); err != nil {
			return "", fmt.Errorf("failed to read message length from net.Conn: %w", err)
		}

	default:
		return "", fmt.Errorf("unsupported connection type: %T", conn)
	}

	// Decode the length of the message from the 2-byte buffer
	messageLength := binary.BigEndian.Uint16(lenBuf[:2])

	// Create a buffer of the appropriate size to hold the message
	messageBuf := make([]byte, messageLength)

	switch c := conn.(type) {
	case net.Conn:
		if _, err := io.ReadFull(c, messageBuf); err != nil {
			return "", fmt.Errorf("failed to read message from net.Conn: %w", err)
		}

	default:
		return "", fmt.Errorf("unsupported connection type: %T", conn)
	}

	// Convert the message buffer to a string and return it
	return string(messageBuf), nil
}

func SendBinaryTransportString(conn interface{}, message string, transport byte) error {
	// Header size
	const headerSize = 3

	// Create a buffer with the appropriate size for the message
	buf := make([]byte, headerSize+len(message))

	// Encode the length of the message as a big-endian 2-byte unsigned integer
	binary.BigEndian.PutUint16(buf[:headerSize], uint16(len(message)))

	// encode the transport tyope
	buf[2] = transport

	// Copy the message into the buffer after the length
	copy(buf[headerSize:], message)

	switch c := conn.(type) {
	case net.Conn:
		// Send the buffer over the connection
		if _, err := c.Write(buf); err != nil {
			return fmt.Errorf("failed to send message: %w", err)
		}

	default:
		// Handle unsupported connection types
		return fmt.Errorf("unsupported connection type: %T", conn)
	}
	// Successful
	return nil
}

func ReceiveBinaryTransportString(conn interface{}) (string, byte, error) {
	// Header size
	const headerSize = 3

	// Create a buffer to read the first 2 bytes (the length of the message)
	lenBuf := make([]byte, headerSize)

	switch c := conn.(type) {
	case net.Conn:
		// Read exactly 2 bytes for the message length
		if _, err := io.ReadFull(c, lenBuf); err != nil {
			return "", 0, fmt.Errorf("failed to read message length from net.Conn: %w", err)
		}

	default:
		return "", 0, fmt.Errorf("unsupported connection type: %T", conn)
	}

	// Decode the length of the message from the 2-byte buffer
	messageLength := binary.BigEndian.Uint16(lenBuf[:2])

	// decode the transport
	transport := lenBuf[2]

	// Create a buffer of the appropriate size to hold the message
	messageBuf := make([]byte, messageLength)

	switch c := conn.(type) {
	case net.Conn:
		if _, err := io.ReadFull(c, messageBuf); err != nil {
			return "", 0, fmt.Errorf("failed to read message from net.Conn: %w", err)
		}

	default:
		return "", 0, fmt.Errorf("unsupported connection type: %T", conn)
	}

	// Convert the message buffer to a string and return it
	return string(messageBuf), transport, nil
}

func SendBinaryByte(conn interface{}, message byte) error {
	// Create a 1-byte buffer and send the message
	messageBuf := [1]byte{message}

	switch c := conn.(type) {
	case net.Conn:
		// "failed to write", because that is what happened. It said "failed to
		// read" here, on the write path, so a control channel that could not be
		// written to reported itself as a read error with a write error inside
		// it — a line nobody could act on.
		if _, err := c.Write(messageBuf[:]); err != nil {
			return fmt.Errorf("failed to write message to net.Conn: %w", err)
		}

	default:
		return fmt.Errorf("unsupported connection type: %T", conn)
	}

	// Successful
	return nil
}

// SendBinaryByteWithin is SendBinaryByte with a bound on how long it may take.
//
// A one-byte write looks instant and is not. It lands in the kernel's send
// buffer, and when the peer has stopped absorbing anything — a path that has
// black-holed, a machine that went away without closing — the buffer fills and
// the write blocks. Nothing returns an error until the kernel gives up
// retransmitting, which on Linux defaults is on the order of fifteen minutes.
//
// For a heartbeat on the control channel that is not a delay, it is the whole
// failure: the server goes on believing it has a control channel, refuses the
// client's attempts to establish a new one because one is "already
// established", cannot ask for pool connections because the request never
// reaches anybody, and drops every user connection with the queue full — for
// as long as the kernel takes. The tunnel is down and the only thing that
// clears it is a restart by hand.
//
// A control channel that cannot take one byte within a few seconds is not a
// control channel. This says so while it is still worth saying.
func SendBinaryByteWithin(conn net.Conn, message byte, timeout time.Duration) error {
	if conn == nil {
		return fmt.Errorf("no connection")
	}
	// A deadline that cannot be set is not a reason to refuse to write: the
	// conn may not support one, and the unbounded write is still better than
	// no write at all.
	if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err == nil {
		defer func() { _ = conn.SetWriteDeadline(time.Time{}) }()
	}
	return SendBinaryByte(conn, message)
}

func ReceiveBinaryByte(conn net.Conn) (byte, error) {
	var messageBuf [1]byte

	switch c := conn.(type) {
	case net.Conn:
		if _, err := io.ReadFull(c, messageBuf[:]); err != nil {
			return 0, fmt.Errorf("failed to read message from net.Conn: %w", err)
		}

	default:
		return 0, fmt.Errorf("unsupported connection type: %T", conn)
	}

	// Convert the message buffer to a string and return it
	return messageBuf[0], nil
}
