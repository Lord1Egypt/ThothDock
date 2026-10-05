package portmap

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

func echoServer(t *testing.T) (port int, stop func()) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, func() { ln.Close() }
}

func TestForwardsBytesBothWaysWithHalfClose(t *testing.T) {
	cp, stop := echoServer(t)
	defer stop()
	f, err := Start(Binding{HostIP: "127.0.0.1", ContainerPort: cp})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.Binding.HostPort == 0 {
		t.Fatal("ephemeral port not reported")
	}
	c, err := net.Dial("tcp", f.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	payload := bytes.Repeat([]byte("thoth"), 100000) // 500 KB
	go func() {
		c.Write(payload)
		c.(*net.TCPConn).CloseWrite() // half-close: the echo must still come back
	}()
	got, err := io.ReadAll(c)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("echoed %d bytes (err %v), want %d", len(got), err, len(payload))
	}
}

func TestDefaultsToLoopbackOnlyWhenAsked(t *testing.T) {
	f, err := Start(Binding{HostIP: "127.0.0.1", ContainerPort: 9})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if h, _, _ := net.SplitHostPort(f.ln.Addr().String()); h != "127.0.0.1" {
		t.Fatalf("bound to %s", h)
	}
}

func TestCollisionAndReleaseOnClose(t *testing.T) {
	cp, stop := echoServer(t)
	defer stop()
	f, err := Start(Binding{HostIP: "127.0.0.1", ContainerPort: cp})
	if err != nil {
		t.Fatal(err)
	}
	port := f.Binding.HostPort
	_, err = Start(Binding{HostIP: "127.0.0.1", HostPort: port, ContainerPort: cp})
	var ae *AllocatedError
	if !errors.As(err, &ae) {
		t.Fatalf("second bind on %d: %v", port, err)
	}
	// An open connection must not keep the port after Close.
	c, _ := net.Dial("tcp", f.Addr())
	f.Close()
	f.Close() // idempotent
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection survived Close")
	}
	c.Close()
	again, err := Start(Binding{HostIP: "127.0.0.1", HostPort: port, ContainerPort: cp})
	if err != nil {
		t.Fatalf("port %d not released: %v", port, err)
	}
	again.Close()
	if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second); err == nil {
		c.Close()
		t.Fatal("listener orphaned after Close")
	}
}

func TestNothingListeningInsideDropsTheClient(t *testing.T) {
	// A port nothing listens on: grab one and release it.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := l.Addr().(*net.TCPAddr).Port
	l.Close()
	f, _ := Start(Binding{HostIP: "127.0.0.1", ContainerPort: dead})
	defer f.Close()
	c, err := net.Dial("tcp", f.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestConnectionLimit(t *testing.T) {
	cp, stop := echoServer(t)
	defer stop()
	f, _ := Start(Binding{HostIP: "127.0.0.1", ContainerPort: cp})
	defer f.Close()
	var held []net.Conn
	defer func() {
		for _, c := range held {
			c.Close()
		}
	}()
	for i := 0; i < maxConns+20; i++ {
		c, err := net.Dial("tcp", f.Addr())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	time.Sleep(300 * time.Millisecond)
	f.mu.Lock()
	n := f.active
	f.mu.Unlock()
	if n > maxConns {
		t.Fatalf("%d active connections, limit %d", n, maxConns)
	}
	// The surplus connections were closed by the forwarder.
	closed := 0
	for _, c := range held[maxConns-10:] {
		c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		if _, err := c.Read(make([]byte, 1)); err == io.EOF {
			closed++
		}
	}
	if closed < 20 {
		t.Fatalf("only %d surplus connections were refused", closed)
	}
}
