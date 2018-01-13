// Package pool implements a pool of net.Conn interfaces to manage and reuse them.
package pool

import (
	"errors"
	"net/textproto"
)

var (
	// ErrClosed is the error resulting if the pool is closed via pool.Close().
	ErrClosed = errors.New("pool is closed")
)

// Pool interface describes a pool implementation. A pool should have maximum
// capacity. An ideal pool is threadsafe and easy to use.
type Pool interface {
	// Get returns a new connection from the pool. Closing the connections puts
	// it back to the Pool. Closing it when the pool is destroyed or full will
	// be counted as an error.
	Get() (*textproto.Conn, error)

	Return(*textproto.Conn)
}
