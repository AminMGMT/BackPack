package smux

import (
	"io"
	"net"
	"testing"
	"time"
)

func req(class CLASSID, sid uint32, seq uint32) writeRequest {
	return writeRequest{class: class, seq: seq, frame: Frame{sid: sid}}
}

func popAll(sq *shaperQueue) (out []writeRequest) {
	for {
		r, ok := sq.Pop()
		if !ok {
			return
		}
		out = append(out, r)
	}
}

// Control first, then priority data, then the round robin.
func TestShaperPriorityOrder(t *testing.T) {
	sq := NewShaperQueue()
	sq.Push(req(CLSDATA, 1, 1))
	sq.Push(req(CLSDATA, 2, 2))
	sq.Push(req(CLSPRIO, 9, 3))
	sq.Push(req(CLSCTRL, 5, 4))
	sq.Push(req(CLSPRIO, 8, 5))
	var sids []uint32
	for _, r := range popAll(sq) {
		sids = append(sids, r.frame.sid)
	}
	want := []uint32{5, 9, 8, 1, 2}
	for i := range want {
		if sids[i] != want[i] {
			t.Fatalf("order %v, want %v", sids, want)
		}
	}
	if !sq.IsEmpty() {
		t.Fatalf("queue not empty: %d", sq.Len())
	}
}

// A flood of priority frames cannot stop ordinary data: one data frame goes out
// after every prioBurst priority ones.
func TestShaperPriorityBurstBound(t *testing.T) {
	sq := NewShaperQueue()
	for i := 0; i < 3*prioBurst; i++ {
		sq.Push(req(CLSPRIO, 9, uint32(i)))
	}
	sq.Push(req(CLSDATA, 1, 100))
	sq.Push(req(CLSDATA, 2, 101))
	out := popAll(sq)
	if len(out) != 3*prioBurst+2 {
		t.Fatalf("popped %d", len(out))
	}
	run, firstData := 0, -1
	for i, r := range out {
		if r.class == CLSPRIO {
			run++
			if run > prioBurst {
				t.Fatalf("%d priority frames in a row at %d", run, i)
			}
			continue
		}
		run = 0
		if firstData < 0 {
			firstData = i
		}
	}
	if firstData != prioBurst {
		t.Fatalf("first data frame at %d, want %d", firstData, prioBurst)
	}
	// Priority frames keep their arrival order.
	last := -1
	for _, r := range out {
		if r.class == CLSPRIO {
			if int(r.seq) <= last {
				t.Fatalf("priority frames reordered")
			}
			last = int(r.seq)
		}
	}
}

// Priority only changes the send order: a prioritised stream and an ordinary
// one both carry their bytes intact, and the peer needs no setting.
func TestStreamPriorityEndToEnd(t *testing.T) {
	a, b := net.Pipe()
	cfg := DefaultConfig()
	cfg.Version = 2
	cfg.KeepAliveDisabled = true
	cs, err := Client(a, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ss, err := Server(b, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	defer ss.Close()

	done := make(chan error, 2)
	go func() {
		for i := 0; i < 2; i++ {
			st, err := ss.AcceptStream()
			if err != nil {
				done <- err
				return
			}
			go func() {
				buf := make([]byte, 4)
				_, err := io.ReadFull(st, buf)
				if err == nil {
					_, err = st.Write(buf)
				}
				st.Close()
				done <- err
			}()
		}
	}()
	for _, prio := range []bool{true, false} {
		st, err := cs.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		st.SetPriority(prio)
		if _, err := st.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		_ = st.SetReadDeadline(time.Now().Add(5 * time.Second))
		got := make([]byte, 4)
		if _, err := io.ReadFull(st, got); err != nil || string(got) != "ping" {
			t.Fatalf("prio=%v echo %q err %v", prio, got, err)
		}
		st.Close()
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
