package netcap

import (
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestLimitHoldsExtraConnectionsUntilOneCloses(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := Limit(inner, 2)
	defer l.Close()

	var accepted atomic.Int32
	conns := make(chan net.Conn, 4)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			conns <- c
		}
	}()
	for range 3 {
		c, err := net.Dial("tcp", inner.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
	}

	waitFor := func(n int32) {
		t.Helper()
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if accepted.Load() == n {
				return
			}
		}
		t.Fatalf("accepted %d, want %d", accepted.Load(), n)
	}
	waitFor(2)
	time.Sleep(50 * time.Millisecond)
	if n := accepted.Load(); n != 2 {
		t.Fatalf("accepted %d past the cap of 2", n)
	}
	c := <-conns
	c.Close()
	c.Close() // a second Close must not free a second slot
	waitFor(3)

	d, err := net.Dial("tcp", inner.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	time.Sleep(50 * time.Millisecond)
	if n := accepted.Load(); n != 3 {
		t.Fatalf("accepted %d, want 3: closing one connection twice freed two slots", n)
	}
}
