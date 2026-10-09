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
	ctSpeedStreams  = 4
	ctSpeedBlock    = 32 << 10
	ctSpeedWarmup   = 2 * time.Second
	ctSpeedMinimum  = 2 * time.Second
	ctSpeedMaximum  = 4 * time.Second
	ctSpeedInterval = 500 * time.Millisecond
	// Includes concurrent connection setup, both directions and cleanup.
	ctSpeedBudget = 20 * time.Second
)

type ctSpeedResult struct {
	download, upload float64
	stable           bool
}

// ctSpeed measures one tunnel at a time. Streams share the same sampling
// window; warmup bytes, socket buffers and echo traffic are never counted.
func ctSpeed(ctx context.Context, dial func() (net.Conn, error)) (ctSpeedResult, error) {
	var r ctSpeedResult
	ctx, cancel := context.WithTimeout(ctx, ctSpeedBudget)
	defer cancel()
	download, downStable, err := ctSpeedDirection(ctx, dial, 'D')
	if err != nil {
		return r, fmt.Errorf("download: %w", err)
	}
	upload, upStable, err := ctSpeedDirection(ctx, dial, 'U')
	if err != nil {
		return r, fmt.Errorf("upload: %w", err)
	}
	r.download, r.upload, r.stable = download, upload, downStable && upStable
	return r, nil
}

func ctSpeedDirection(parent context.Context, dial func() (net.Conn, error), direction byte) (float64, bool, error) {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	var received atomic.Uint64
	var streams [ctSpeedStreams]atomic.Uint64
	ready := make(chan error, ctSpeedStreams)
	failures := make(chan error, ctSpeedStreams)
	// Negotiate all streams concurrently, then release them together.
	start := make(chan struct{})
	for i := range ctSpeedStreams {
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
	for range ctSpeedStreams {
		select {
		case err := <-ready:
			if err != nil {
				return 0, false, err
			}
		case <-ctx.Done():
			return 0, false, ctx.Err()
		}
	}
	close(start)
	warmup := time.NewTimer(ctSpeedWarmup)
	defer warmup.Stop()
	select {
	case <-warmup.C:
	case err := <-failures:
		return 0, false, err
	case <-ctx.Done():
		return 0, false, ctx.Err()
	}
	started, previous := time.Now(), time.Now()
	baseline := received.Load()
	var streamBaseline [ctSpeedStreams]uint64
	for i := range streams {
		streamBaseline[i] = streams[i].Load()
	}
	last := baseline
	tick := time.NewTicker(ctSpeedInterval)
	defer tick.Stop()
	var samples []float64
	for {
		select {
		case <-ctx.Done():
			return 0, false, ctx.Err()
		case err := <-failures:
			return 0, false, err
		case <-tick.C:
			now, count := time.Now(), received.Load()
			samples = append(samples, float64(count-last)/now.Sub(previous).Seconds())
			previous, last = now, count
			elapsed := now.Sub(started)
			stable := ctSpeedConverged(samples)
			if elapsed >= ctSpeedMinimum && stable || elapsed >= ctSpeedMaximum {
				select {
				case err := <-failures:
					return 0, false, err
				default:
				}
				for i := range streams {
					if streams[i].Load() == streamBaseline[i] {
						return 0, false, errors.New("a speed stream carried no verified traffic")
					}
				}
				if count == baseline {
					return 0, false, errors.New("no verified traffic during measurement")
				}
				// Cancellation can race the last sample; never publish a partial run.
				if err := parent.Err(); err != nil {
					return 0, false, err
				}
				return float64(count-baseline) * 8 / elapsed.Seconds() / 1e6, stable, nil
			}
		}
	}
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
	var total uint64
	var ack [8]byte
	err := ctSpeedReceive(c, payload, func(n uint64) error {
		total += n
		binary.BigEndian.PutUint64(ack[:], total)
		_, err := io.Copy(c, bytes.NewReader(ack[:]))
		return err
	})
	if err != nil {
		binary.BigEndian.PutUint64(ack[:], math.MaxUint64)
		_, _ = c.Write(ack[:])
	}
}
