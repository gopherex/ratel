//go:build integration

package tests_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// commitCutter forwards PostgreSQL connections to a server. On the first
// connection whose client sends marker, it forwards the next COMMIT, lets the
// server answer it and closes the client side instead of relaying the
// answer: the server committed, the client sees a broken connection.
type commitCutter struct {
	listener net.Listener
	server   string
	marker   []byte
	cut      atomic.Bool
	wait     sync.WaitGroup
}

func newCommitCutter(ctx context.Context, t *testing.T, server, marker string) *commitCutter {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	cutter := &commitCutter{listener: listener, server: server, marker: []byte(marker)}

	cutter.wait.Go(cutter.accept)
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Error(err)
		}

		cutter.wait.Wait()
	})

	return cutter
}

// address is the host and port clients connect to.
func (c *commitCutter) address(t *testing.T) (string, uint16) {
	t.Helper()

	host, port, err := net.SplitHostPort(c.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		t.Fatal(err)
	}

	return host, uint16(number)
}

func (c *commitCutter) accept() {
	for {
		client, err := c.listener.Accept()
		if err != nil {
			return
		}

		c.wait.Go(func() { c.relay(client) })
	}
}

func (c *commitCutter) relay(client net.Conn) {
	defer client.Close()

	server, err := net.Dial("tcp", c.server)
	if err != nil {
		return
	}

	defer server.Close()

	var cutting atomic.Bool

	c.wait.Go(func() {
		buffer := make([]byte, 64*1024)
		armed := false

		for {
			read, err := client.Read(buffer)
			if err != nil {
				_ = server.Close()

				return
			}

			chunk := buffer[:read]
			armed = armed || bytes.Contains(chunk, c.marker)

			if armed && bytes.Contains(bytes.ToLower(chunk), []byte("commit")) && c.cut.CompareAndSwap(false, true) {
				cutting.Store(true)
			}

			if _, err := server.Write(chunk); err != nil {
				return
			}
		}
	})

	buffer := make([]byte, 64*1024)

	for {
		read, err := server.Read(buffer)
		if err != nil {
			return
		}

		if cutting.Load() {
			return // the server answered the COMMIT; the client never hears it
		}

		if _, err := client.Write(buffer[:read]); err != nil && !errors.Is(err, io.EOF) {
			return
		}
	}
}
