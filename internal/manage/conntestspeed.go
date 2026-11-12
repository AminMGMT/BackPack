package manage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ctSpeedMagic    = "BP-SPEED/1\n"
	ctSpeedAck      = "BP-SPEED/1 OK\n"
	ctSpeedBlock    = 32 << 10
	ctSpeedWarmup   = 2 * time.Second
	ctSpeedMinimum  = 2 * time.Second
	ctSpeedMaximum  = 4 * time.Second
	ctSpeedInterval = 500 * time.Millisecond
	// ctSpeedPeakWindow is the span the peak is taken over: the best this
	// long a stretch the tunnel carried. Shorter would report a socket buffer
	// draining at once, not a rate the path holds.
	ctSpeedPeakWindow = time.Second
	// Includes concurrent connection setup, both stages of both directions
	// and cleanup.
	ctSpeedBudget = 40 * time.Second

	// ctSpeedAckEvery is how often the receiving end of an upload reports its
	// running count. It used to answer every read with eight bytes — a write
	// for each 32 KB received, plus the read for it on the Iran side — so a
	// fast upload spent half its CPU on the counts and the test measured that
	// instead of the tunnel. Measured on loopback, 4 streams: upload 25.6
	// Gb/s with a count per read, 50.5 with one every 5 ms, against 51.0 for
	// a plain bulk copy; download, which has no counts, 53.5 either way. The
	// Iran side samples every ctSpeedInterval, so a count at most this old is
	// as good as one per read, and older Iran servers read the counts the
	// same way however often they come.
	ctSpeedAckEvery = 5 * time.Millisecond
)

// ctSpeedStages are the connection counts each direction is measured with,
// in order. Four is what one user's few connections look like; sixteen loads
// the tunnel until the path, not the number of connections, is what limits
// it — on a long round trip a handful of TCP windows cannot fill a fast link,
// and the figure that answers "how much can this tunnel carry" is the loaded
// one. Both are run and the better kept: some carriers do worse with more
// connections, and that should not be read as their limit.
var ctSpeedStages = []int{4, 16}

type ctSpeedResult struct {
	download, upload float64
	// downloadPeak and uploadPeak are the most the tunnel carried over any
	// ctSpeedPeakWindow of either stage: the maximum it can be got to move.
	downloadPeak, uploadPeak float64
	// streams is how many connections the download figure came from.
	streams int
	stable  bool
}

// ctSpeed measures one tunnel at a time. Streams share the same sampling
// window; warmup bytes, socket buffers and echo traffic are never counted.
func ctSpeed(ctx context.Context, dial func() (net.Conn, error)) (ctSpeedResult, error) {
	var r ctSpeedResult
	ctx, cancel := context.WithTimeout(ctx, ctSpeedBudget)
	defer cancel()
	down, err := ctSpeedStaged(ctx, dial, 'D')
	if err != nil {
		return r, fmt.Errorf("download: %w", err)
	}
	up, err := ctSpeedStaged(ctx, dial, 'U')
	if err != nil {
		return r, fmt.Errorf("upload: %w", err)
	}
	r.download, r.downloadPeak, r.streams = down.avg, down.peak, down.streams
	r.upload, r.uploadPeak = up.avg, up.peak
	r.stable = down.stable && up.stable
	return r, nil
}

// ctSpeedRun is one direction measured at one connection count.
type ctSpeedRun struct {
	avg, peak float64
	streams   int
	stable    bool
}

// ctSpeedStaged measures one direction at every stage and keeps the best.
//
// The first stage has to succeed: it is the measurement the test has always
// made, and a tunnel that cannot carry it has failed the speed test. A later
// stage may fail — a carrier whose pool will not open sixteen connections at
// once is still a carrier that works — and then the earlier figures stand.
func ctSpeedStaged(ctx context.Context, dial func() (net.Conn, error), direction byte) (ctSpeedRun, error) {
	var best ctSpeedRun
	for i, n := range ctSpeedStages {
		run, err := ctSpeedDirection(ctx, dial, direction, n)
		if err != nil {
			if i == 0 {
				return best, err
			}
			if ctx.Err() != nil && ctx.Err() != context.DeadlineExceeded {
				return best, ctx.Err()
			}
			break
		}
		if run.avg > best.avg {
			peak := max(best.peak, run.peak)
			best = run
			best.peak = peak
		} else {
			best.peak = max(best.peak, run.peak)
		}
	}
	return best, nil
}

func ctSpeedDirection(parent context.Context, dial func() (net.Conn, error), direction byte, n int) (ctSpeedRun, error) {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	var received atomic.Uint64
	streams := make([]atomic.Uint64, n)
	ready := make(chan error, n)
	failures := make(chan error, n)
	// Negotiate all streams concurrently, then release them together.
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := dial()
			if err != nil {
				ready <- err
				return
			}
			defer c.Close()
			stop := context.AfterFunc(ctx, func() { c.Close() })
			defer stop()
			_ = c.SetDeadline(time.Now().Add(3 * time.Second))
			payload := make([]byte, ctSpeedBlock)
			if _, err = rand.Read(payload); err == nil {
				_, err = io.Copy(c, bytes.NewReader(append([]byte(ctSpeedMagic+string(direction)), payload...)))
			}
			ack := make([]byte, len(ctSpeedAck))
			if err == nil {
				_, err = io.ReadFull(c, ack)
			}
			if err == nil && string(ack) != ctSpeedAck {
				err = errors.New("kharej does not support the speed test; update Backpack on both servers")
			}
			ready <- err
			if err != nil {
				return
			}
			select {
			case <-start:
			case <-ctx.Done():
				return
			}
			_ = c.SetDeadline(time.Now().Add(ctSpeedWarmup + ctSpeedMaximum + 2*time.Second))
			if _, err = c.Write([]byte{1}); err != nil {
				failures <- err
				return
			}
			if direction == 'D' {
				err = ctSpeedReceive(c, payload, func(n uint64) error { streams[i].Add(n); received.Add(n); return nil })
			} else {
				// Acknowledgements count verified bytes at the remote receiver,
				// not bytes accepted into this machine's send buffer.
				writer := make(chan error, 1)
				go func() { writer <- ctSpeedSend(c, payload) }()
				var prior uint64
				var ack [8]byte
				for {
					if _, err = io.ReadFull(c, ack[:]); err != nil {
						break
					}
					n := binary.BigEndian.Uint64(ack[:])
					if n == math.MaxUint64 || n < prior {
						err = errors.New("upload payload failed verification")
						break
					}
					received.Add(n - prior)
					streams[i].Add(n - prior)
					prior = n
				}
				c.Close()
				<-writer
			}
			if ctx.Err() == nil {
				failures <- err
			}
		}()
	}
	for range n {
		select {
		case err := <-ready:
			if err != nil {
				return ctSpeedRun{}, err
			}
		case <-ctx.Done():
			return ctSpeedRun{}, ctx.Err()
		}
	}
	close(start)
	warmup := time.NewTimer(ctSpeedWarmup)
	defer warmup.Stop()
	select {
	case <-warmup.C:
	case err := <-failures:
		return ctSpeedRun{}, err
	case <-ctx.Done():
		return ctSpeedRun{}, ctx.Err()
	}
	started, previous := time.Now(), time.Now()
	baseline := received.Load()
	streamBaseline := make([]uint64, n)
	for i := range streams {
		streamBaseline[i] = streams[i].Load()
	}
	last := baseline
	tick := time.NewTicker(ctSpeedInterval)
	defer tick.Stop()
	var samples []float64
	var ticks []ctSpeedTick
	for {
		select {
		case <-ctx.Done():
			return ctSpeedRun{}, ctx.Err()
		case err := <-failures:
			return ctSpeedRun{}, err
		case <-tick.C:
			now, count := time.Now(), received.Load()
			samples = append(samples, float64(count-last)/now.Sub(previous).Seconds())
			ticks = append(ticks, ctSpeedTick{bytes: count - last, span: now.Sub(previous)})
			previous, last = now, count
			elapsed := now.Sub(started)
			stable := ctSpeedConverged(samples)
			if elapsed >= ctSpeedMinimum && stable || elapsed >= ctSpeedMaximum {
				select {
				case err := <-failures:
					return ctSpeedRun{}, err
				default:
				}
				for i := range streams {
					if streams[i].Load() == streamBaseline[i] {
						return ctSpeedRun{}, errors.New("a speed stream carried no verified traffic")
					}
				}
				if count == baseline {
					return ctSpeedRun{}, errors.New("no verified traffic during measurement")
				}
				// Cancellation can race the last sample; never publish a partial run.
				if err := parent.Err(); err != nil {
					return ctSpeedRun{}, err
				}
				avg := float64(count-baseline) * 8 / elapsed.Seconds() / 1e6
				return ctSpeedRun{avg: avg, peak: max(avg, ctSpeedPeak(ticks)), streams: n, stable: stable}, nil
			}
		}
	}
}

// ctSpeedTick is what one sampling interval carried.
type ctSpeedTick struct {
	bytes uint64
	span  time.Duration
}

// ctSpeedPeak is the best rate, in Mb/s, over any run of consecutive ticks
// spanning at least ctSpeedPeakWindow. Zero when the run was shorter than one
// window, which the caller covers with the average.
func ctSpeedPeak(ticks []ctSpeedTick) float64 {
	var best float64
	for i := range ticks {
		var b uint64
		var span time.Duration
		for j := i; j < len(ticks); j++ {
			b += ticks[j].bytes
			span += ticks[j].span
			if span >= ctSpeedPeakWindow {
				best = max(best, float64(b)*8/span.Seconds()/1e6)
				break
			}
		}
	}
	return best
}

func ctSpeedConverged(samples []float64) bool {
	if len(samples) < 4 {
		return false
	}
	lo, hi := math.Inf(1), float64(0)
	for _, n := range samples[len(samples)-4:] {
		lo, hi = math.Min(lo, n), math.Max(hi, n)
	}
	return lo > 0 && hi <= lo*1.10
}

func ctSpeedSend(c net.Conn, payload []byte) error {
	for {
		if _, err := io.Copy(c, bytes.NewReader(payload)); err != nil {
			return err
		}
	}
}

func ctSpeedReceive(c net.Conn, payload []byte, count func(uint64) error) error {
	buf := make([]byte, len(payload))
	pos := 0
	for {
		n, err := c.Read(buf[:len(payload)-pos])
		if n > 0 {
			if !bytes.Equal(buf[:n], payload[pos:pos+n]) {
				return errors.New("speed payload came back different")
			}
			if e := count(uint64(n)); e != nil {
				return e
			}
			pos = (pos + n) % len(payload)
		}
		if err != nil {
			return err
		}
	}
}

// Ordinary random echo probes still work, including those from older Iran
// binaries. Only the reserved prefix switches a disposable echo connection
// into a bounded, directional speed stream.
func ctServeSpeedOrEcho(c net.Conn) {
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	head := make([]byte, len(ctSpeedMagic))
	if _, err := io.ReadFull(c, head); err != nil {
		return
	}
	if string(head) != ctSpeedMagic {
		_ = c.SetDeadline(time.Time{})
		if _, err := io.Copy(c, bytes.NewReader(head)); err == nil {
			_, _ = io.Copy(c, c)
		}
		return
	}
	var direction [1]byte
	if _, err := io.ReadFull(c, direction[:]); err != nil {
		return
	}
	if direction[0] != 'D' && direction[0] != 'U' {
		return
	}
	payload := make([]byte, ctSpeedBlock)
	if _, err := io.ReadFull(c, payload); err != nil {
		return
	}
	if _, err := io.Copy(c, bytes.NewBufferString(ctSpeedAck)); err != nil {
		return
	}
	var start [1]byte
	if _, err := io.ReadFull(c, start[:]); err != nil || start[0] != 1 {
		return
	}
	_ = c.SetDeadline(time.Now().Add(ctSpeedWarmup + ctSpeedMaximum + 2*time.Second))
	if direction[0] == 'D' {
		_ = ctSpeedSend(c, payload)
		return
	}
	// The running count goes back every ctSpeedAckEvery from a goroutine of
	// its own, and only when it has moved; the reader only adds to it.
	var total atomic.Uint64
	stop, acked := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(acked)
		tick := time.NewTicker(ctSpeedAckEvery)
		defer tick.Stop()
		var sent uint64
		var ack [8]byte
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			if n := total.Load(); n != sent {
				binary.BigEndian.PutUint64(ack[:], n)
				if _, err := c.Write(ack[:]); err != nil {
					return
				}
				sent = n
			}
		}
	}()
	// ctSpeedReceive returns only when the stream ends — a read error, bytes
	// that came back different, or the deadline — so the failure marker always
	// follows it. A client that finished measuring has already closed the
	// connection and stopped counting; one still measuring is told to stop.
	_ = ctSpeedReceive(c, payload, func(n uint64) error {
		total.Add(n)
		return nil
	})
	close(stop)
	<-acked
	// The last count, then the marker: a count never trails what arrived.
	var ack [8]byte
	binary.BigEndian.PutUint64(ack[:], total.Load())
	_, _ = c.Write(ack[:])
	binary.BigEndian.PutUint64(ack[:], math.MaxUint64)
	_, _ = c.Write(ack[:])
}
