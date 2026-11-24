package client

import (
	"net"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// maintainPool is Backhaul's poolMaintainer, which each of its client
// transports carried a copy of: fill the pool, then every ten seconds compare
// how many connections the server asked for with how many sat idle, grow by
// one when it asked for more, and shrink by one (by skipping the next
// request's dial) when it asked for far fewer.
func maintainPool(g *clientGen, poolSize int, aggressive bool, logger *logrus.Logger, dial func()) {
	for i := 0; i < poolSize; i++ { //initial pool filling
		go dial()
	}

	a := 4
	b := 5
	x := 3
	y := 4.0

	if aggressive {
		logger.Info("aggressive pool management enabled")
		a = 1
		b = 2
		x = 0
		y = 0.75
	}

	tickerPool := time.NewTicker(time.Second * 1)
	defer tickerPool.Stop()

	tickerLoad := time.NewTicker(time.Second * 10)
	defer tickerLoad.Stop()

	newPoolSize := poolSize // intial value
	var poolConnectionsSum int32 = 0

	for {
		select {
		case <-g.ctx.Done():
			return

		case <-tickerPool.C:
			atomic.AddInt32(&poolConnectionsSum, atomic.LoadInt32(&g.poolConnections))

		case <-tickerLoad.C:
			loadConnections := (int(atomic.LoadInt32(&g.loadConnections)) + 9) / 10 // +9 for ceil-like logic
			atomic.StoreInt32(&g.loadConnections, 0)                                // Reset

			poolConnectionsAvg := (int(atomic.LoadInt32(&poolConnectionsSum)) + 9) / 10 // +9 for ceil-like logic
			atomic.StoreInt32(&poolConnectionsSum, 0)                                   // Reset

			if (loadConnections + a) > poolConnectionsAvg*b {
				logger.Debugf("increasing pool size: %d -> %d, avg pool conn: %d, avg load conn: %d", newPoolSize, newPoolSize+1, poolConnectionsAvg, loadConnections)
				newPoolSize++

				go dial()
			} else if float64(loadConnections+x) < float64(poolConnectionsAvg)*y && newPoolSize > poolSize {
				logger.Debugf("decreasing pool size: %d -> %d, avg pool conn: %d, avg load conn: %d", newPoolSize, newPoolSize-1, poolConnectionsAvg, loadConnections)
				newPoolSize--

				g.controlFlow <- struct{}{}
			}
		}
	}
}

// netControl is a generation's control connection, set by the dialer and read
// by the handler and by Restart.
type netControl struct{ p atomic.Pointer[net.Conn] }

func (c *netControl) Set(conn net.Conn) { c.p.Store(&conn) }

func (c *netControl) Get() net.Conn {
	if p := c.p.Load(); p != nil {
		return *p
	}
	return nil
}

// wsControl is the same for a websocket control channel.
type wsControl struct {
	p atomic.Pointer[websocket.Conn]
}

func (c *wsControl) Set(conn *websocket.Conn) { c.p.Store(conn) }
func (c *wsControl) Get() *websocket.Conn     { return c.p.Load() }
