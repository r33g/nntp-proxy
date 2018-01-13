package pool

import (
	"errors"
	"fmt"
	"net/textproto"
	"sync"
	"time"
)

// channelPool implements the Pool interface based on buffered channels.
type channelPool struct {
	// storage for our net.Conn connections
	mu    sync.Mutex
	conns chan *textproto.Conn

	// net.Conn generator
	factory   Factory
	max       int
	current   int
	createsem chan bool
}

// Factory is a function to create new connections.
type Factory func() (*textproto.Conn, error)

// NewChannelPool returns a new pool based on buffered channels with an initial
// capacity and maximum capacity. Factory is used when initial capacity is
// greater than zero to fill the pool. A zero initialCap doesn't fill the Pool
// until a new Get() is called. During a Get(), If there is no new connection
// available in the pool, a new connection will be created via the Factory()
// method.
func NewChannelPool(initialCap, maxCap int, factory Factory) (Pool, error) {
	if initialCap < 0 || maxCap <= 0 || initialCap > maxCap {
		return nil, errors.New("invalid capacity settings")
	}

	c := &channelPool{
		conns:     make(chan *textproto.Conn, maxCap),
		factory:   factory,
		max:       maxCap,
		current:   initialCap,
		createsem: make(chan bool, maxCap),
	}

	// create initial connections, if something goes wrong,
	// just close the pool error out.
	for i := 0; i < initialCap; i++ {
		conn, err := factory()
		if err != nil {
			return nil, fmt.Errorf("factory is not able to fill the pool: %s", err)
		}
		c.conns <- conn
	}

	return c, nil
}

// Get implements the Pool interfaces Get() method. If there is no new
// connection available in the pool, a new connection will be created via the
// Factory() method.
func (c *channelPool) Get() (*textproto.Conn, error) {

	if c.conns == nil {
		return nil, ErrClosed
	}

	// ** handle these errors better **/
	// wrap our connections with out custom net.Conn implementation (wrapConn
	// method) that puts the connection back to the pool if it's closed.
	select {
	case conn := <-c.conns:
		if conn == nil {
			conn, err := c.factory()
			if err != nil {
				//log.Printf("failed")
				// On error, release our create hold
				<-c.createsem
			}
			return conn, err
		}
		return conn, nil

	case <-time.After(time.Millisecond):
		select {
		case conn := <-c.conns:
			if conn == nil {
				conn, err := c.factory()
				if err != nil {
					//log.Printf("failed")
					// On error, release our create hold
					<-c.createsem
				}
				return conn, err
			}
			return conn, nil
		case c.createsem <- true:

			// Room to make a connection
			conn, err := c.factory()
			if err != nil {
				//log.Printf("failed")
				// On error, release our create hold
				<-c.createsem
			}
			return conn, err
		}
	}
}

func (c *channelPool) Return(conn *textproto.Conn) {

	// let the pool know we can make a new connection
	if conn == nil {
		<-c.createsem
		return
	}
	select {
	case c.conns <- conn:
		//log.Printf("returned")
	default:
		//log.Printf("closed")
		// Overflow connection.
		//<-c.createsem
		//c.Close()
	}
}
