package core

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

// The control signals, one byte each, exactly as Backhaul numbers them.
const (
	SG_HB     byte = iota // for heartbeat
	SG_Chan               // for channel, req a new conn
	SG_Ping               // for ping
	SG_Closed             // for closed channel
	SG_TCP                // TCP Transport ID
	SG_UDP                // UDP Transport ID
	SG_RTT                // For RTT measurment
)

// SendBinaryString writes message behind a 2-byte big-endian length.
func SendBinaryString(conn net.Conn, message string) error {
	const headerSize = 2
	buf := make([]byte, headerSize+len(message))
	binary.BigEndian.PutUint16(buf[:headerSize], uint16(len(message)))
	copy(buf[headerSize:], message)
	if _, err := conn.Write(buf); err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	return nil
}

// ReceiveBinaryString reads what SendBinaryString wrote.
func ReceiveBinaryString(conn net.Conn) (string, error) {
	const headerSize = 2
	lenBuf := make([]byte, headerSize)
	if _, err := io.ReadFull(conn, lenBuf); err != nil {
		return "", fmt.Errorf("failed to read message length from net.Conn: %w", err)
	}
	messageBuf := make([]byte, binary.BigEndian.Uint16(lenBuf[:2]))
	if _, err := io.ReadFull(conn, messageBuf); err != nil {
		return "", fmt.Errorf("failed to read message from net.Conn: %w", err)
	}
	return string(messageBuf), nil
}

// SendBinaryTransportString writes message behind a 2-byte length and a
// one-byte transport signal.
func SendBinaryTransportString(conn net.Conn, message string, transport byte) error {
	const headerSize = 3
	buf := make([]byte, headerSize+len(message))
	binary.BigEndian.PutUint16(buf[:headerSize], uint16(len(message)))
	buf[2] = transport
	copy(buf[headerSize:], message)
	if _, err := conn.Write(buf); err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	return nil
}

// ReceiveBinaryTransportString reads what SendBinaryTransportString wrote.
func ReceiveBinaryTransportString(conn net.Conn) (string, byte, error) {
	const headerSize = 3
	lenBuf := make([]byte, headerSize)
	if _, err := io.ReadFull(conn, lenBuf); err != nil {
		return "", 0, fmt.Errorf("failed to read message length from net.Conn: %w", err)
	}
	messageLength := binary.BigEndian.Uint16(lenBuf[:2])
	transport := lenBuf[2]
	messageBuf := make([]byte, messageLength)
	if _, err := io.ReadFull(conn, messageBuf); err != nil {
		return "", 0, fmt.Errorf("failed to read message from net.Conn: %w", err)
	}
	return string(messageBuf), transport, nil
}

// SendBinaryByte writes one signal byte.
func SendBinaryByte(conn net.Conn, message byte) error {
	messageBuf := [1]byte{message}
	if _, err := conn.Write(messageBuf[:]); err != nil {
		return fmt.Errorf("failed to read message from net.Conn: %w", err)
	}
	return nil
}

// ReceiveBinaryByte reads one signal byte.
func ReceiveBinaryByte(conn net.Conn) (byte, error) {
	var messageBuf [1]byte
	if _, err := io.ReadFull(conn, messageBuf[:]); err != nil {
		return 0, fmt.Errorf("failed to read message from net.Conn: %w", err)
	}
	return messageBuf[0], nil
}
