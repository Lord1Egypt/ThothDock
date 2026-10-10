// Package portmap publishes container TCP ports on the host in user space.
//
// ThothDock containers have no network namespace. A container on the device
// network binds the device's own addresses; one on a user-defined network
// binds its own loopback address (PRoot --net-ip). Publishing is a plain TCP
// forwarder from <host address>:<host port> to 127.0.0.1:<container port>,
// or to the container's own address. It binds the loopback address unless
// told otherwise and needs no privilege.
package portmap

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

const (
	// maxConns bounds simultaneous connections through one mapping.
	maxConns    = 256
	dialTimeout = 3 * time.Second
)

// Binding is one published port.
type Binding struct {
	HostIP        string // a literal IP address
	HostPort      int    // 0: the system picks one
	ContainerPort int
	// Target is the address dialled for each connection; empty means
	// 127.0.0.1:ContainerPort (a container on the device network).
	Target string
}

// Forwarder serves one Binding until Close.
type Forwarder struct {
	Binding Binding // HostPort is the port actually bound

	ln     net.Listener
	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	active int // client connections being served; bounded by maxConns
	closed bool
	wg     sync.WaitGroup
}

// AllocatedError reports a host port that is taken.
type AllocatedError struct {
	Addr string
	Err  error
}

func (e *AllocatedError) Error() string {
	return fmt.Sprintf("listen tcp %s: port is already allocated or not permitted: %v", e.Addr, e.Err)
}
func (e *AllocatedError) Unwrap() error { return e.Err }

// Start binds the host side and starts forwarding to the container side.
func Start(b Binding) (*Forwarder, error) {
	addr := net.JoinHostPort(b.HostIP, strconv.Itoa(b.HostPort))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, &AllocatedError{Addr: addr, Err: err}
	}
	f := &Forwarder{Binding: b, ln: ln, conns: map[net.Conn]struct{}{}}
	f.Binding.HostPort = ln.Addr().(*net.TCPAddr).Port
	f.wg.Add(1)
	go f.accept()
	return f, nil
}

func (f *Forwarder) accept() {
	defer f.wg.Done()
	for {
		c, err := f.ln.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return // closed
		}
		f.mu.Lock()
		if f.closed || f.active >= maxConns {
			f.mu.Unlock()
			c.Close()
			continue
		}
		f.active++
		f.conns[c] = struct{}{}
		f.wg.Add(1)
		f.mu.Unlock()
		go f.serve(c)
	}
}

func (f *Forwarder) serve(client net.Conn) {
	defer f.wg.Done()
	defer func() {
		client.Close()
		f.mu.Lock()
		delete(f.conns, client)
		f.active--
		f.mu.Unlock()
	}()
	addr := f.Binding.Target
	if addr == "" {
		addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(f.Binding.ContainerPort))
	}
	target, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return // nothing listens inside the container (yet): drop the client
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		target.Close()
		return
	}
	f.conns[target] = struct{}{}
	f.mu.Unlock()
	defer func() {
		target.Close()
		f.mu.Lock()
		delete(f.conns, target)
		f.mu.Unlock()
	}()
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite() // propagate EOF, keep the other direction open
		}
		done <- struct{}{}
	}
	go pipe(target, client)
	go pipe(client, target)
	<-done
	<-done
}

// Close stops listening, ends every connection and waits for the goroutines.
func (f *Forwarder) Close() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	f.ln.Close()
	for c := range f.conns {
		c.Close()
	}
	f.mu.Unlock()
	f.wg.Wait()
}

// Addr is the host address being served.
func (f *Forwarder) Addr() string {
	return net.JoinHostPort(f.Binding.HostIP, strconv.Itoa(f.Binding.HostPort))
}
