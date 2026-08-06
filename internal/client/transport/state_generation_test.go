package transport

import (
	"context"
	"net"
	"testing"
)

func TestClientStateRejectsRetiredGenerationConnection(t *testing.T) {
	oldCtx, oldCancel := context.WithCancel(context.Background())
	var state clientState
	state.Reset(oldCtx, oldCancel, nil)

	newCtx, newCancel := context.WithCancel(context.Background())
	defer newCancel()
	state.Reset(newCtx, newCancel, nil)

	late, peer := net.Pipe()
	defer late.Close()
	defer peer.Close()
	if state.SetConnFor(oldCtx, late) {
		t.Fatal("retired generation published a late control connection")
	}
	if state.Conn() != nil {
		t.Fatal("late retired connection replaced the current control connection")
	}
	if state.IsCurrent(oldCtx) || !state.IsCurrent(newCtx) {
		t.Fatal("generation identity check returned the wrong result")
	}
}
