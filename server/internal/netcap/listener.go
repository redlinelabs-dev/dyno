// Package netcap caps how many connections a listener holds open at once.
package netcap

import (
	"net"
	"sync"
)

// Limit returns a listener that accepts at most n connections at a time.
// When n are open, Accept waits for one to close; new clients queue in the
// kernel's backlog meanwhile instead of being refused.
func Limit(l net.Listener, n int) net.Listener {
	return &listener{Listener: l, sem: make(chan struct{}, n)}
}

type listener struct {
	net.Listener
	sem chan struct{}
}

func (l *listener) Accept() (net.Conn, error) {
	l.sem <- struct{}{}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	return &conn{Conn: c, release: func() { <-l.sem }}, nil
}

type conn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *conn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
