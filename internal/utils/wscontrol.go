package utils

import (
	"fmt"

	"github.com/gorilla/websocket"
)

// DecodeWebSocketSignal validates the one-byte binary frames used by the
// WebSocket control channel. Treating arbitrary frames as msg[0] let an empty
// frame panic the process and silently accepted trailing protocol data.
func DecodeWebSocketSignal(messageType int, payload []byte) (byte, error) {
	if messageType != websocket.BinaryMessage {
		return 0, fmt.Errorf("control frame type %d is not binary", messageType)
	}
	if len(payload) != 1 {
		return 0, fmt.Errorf("control frame has %d bytes, want exactly 1", len(payload))
	}
	return payload[0], nil
}
