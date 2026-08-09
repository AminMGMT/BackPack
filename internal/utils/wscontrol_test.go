package utils

import (
	"testing"

	"github.com/gorilla/websocket"
)

func TestDecodeWebSocketSignal(t *testing.T) {
	tests := []struct {
		name    string
		typ     int
		payload []byte
		want    byte
		wantErr bool
	}{
		{name: "binary byte", typ: websocket.BinaryMessage, payload: []byte{SG_HB}, want: SG_HB},
		{name: "empty", typ: websocket.BinaryMessage, payload: nil, wantErr: true},
		{name: "trailing data", typ: websocket.BinaryMessage, payload: []byte{SG_HB, SG_Closed}, wantErr: true},
		{name: "text", typ: websocket.TextMessage, payload: []byte{SG_HB}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeWebSocketSignal(tc.typ, tc.payload)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Fatalf("signal = %d, want %d", got, tc.want)
			}
		})
	}
}
