package utils

import (
	"testing"

	"github.com/gorilla/websocket"
)

// The frame that mattered is the empty one: reading msg[0] out of it took the
// whole process down, and a control channel is reachable by anything holding
// the token.
func TestWebSocketSignal(t *testing.T) {
	tests := []struct {
		name    string
		typ     int
		payload []byte
		want    byte
		wantOK  bool
	}{
		{name: "a heartbeat", typ: websocket.BinaryMessage, payload: []byte{SG_HB}, want: SG_HB, wantOK: true},
		{name: "a channel request", typ: websocket.BinaryMessage, payload: []byte{SG_Chan}, want: SG_Chan, wantOK: true},
		{name: "the highest signal", typ: websocket.BinaryMessage, payload: []byte{SG_Pool}, want: SG_Pool, wantOK: true},

		{name: "an empty frame", typ: websocket.BinaryMessage, payload: []byte{}, wantOK: false},
		{name: "a nil payload", typ: websocket.BinaryMessage, payload: nil, wantOK: false},
		{name: "more than one byte", typ: websocket.BinaryMessage, payload: []byte{SG_HB, SG_Closed}, wantOK: false},
		{name: "a text frame", typ: websocket.TextMessage, payload: []byte{SG_HB}, wantOK: false},
		{name: "a close frame", typ: websocket.CloseMessage, payload: []byte{SG_HB}, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := WebSocketSignal(tt.typ, tt.payload)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Fatalf("signal = %d, want %d", got, tt.want)
			}
		})
	}
}

// Everything that writes a signal in this codebase writes it the same way, so
// everything that writes one has to survive the check that reads it.
func TestEverySignalSurvivesTheFrameItIsSentIn(t *testing.T) {
	for _, signal := range []byte{SG_HB, SG_Chan, SG_Ping, SG_Closed, SG_TCP, SG_UDP, SG_RTT, SG_ChanV2, SG_Pool} {
		got, ok := WebSocketSignal(websocket.BinaryMessage, []byte{signal})
		if !ok || got != signal {
			t.Errorf("signal %d did not survive: got %d, ok %v", signal, got, ok)
		}
	}
}
