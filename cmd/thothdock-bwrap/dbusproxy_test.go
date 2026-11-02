package main

import (
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// Flatpak bundles everything after the program into the --args descriptor,
// the --fd=N pipe included, so that is where the pipe is found.
func TestPlanDBusProxyInlinesTheArgumentsAndTakesThePipe(t *testing.T) {
	read := func(fd int) ([]string, error) {
		if fd != 7 {
			t.Errorf("read fd %d", fd)
		}
		return []string{"--fd=5", "unix:path=/var/run/dbus/system_bus_socket", "/run/user/1000/.dbus-proxy/system-bus-proxy-AB12", "--filter", "--talk=org.freedesktop.login1"}, nil
	}
	argv, plan, err := planDBusProxy([]string{"xdg-dbus-proxy", "--args=7"}, read)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"xdg-dbus-proxy", "unix:path=/var/run/dbus/system_bus_socket", "/run/user/1000/.dbus-proxy/system-bus-proxy-AB12", "--filter", "--talk=org.freedesktop.login1"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv %q", argv)
	}
	if plan == nil || plan.syncFD != 5 || plan.socket != "/run/user/1000/.dbus-proxy/system-bus-proxy-AB12" {
		t.Fatalf("plan %+v", plan)
	}
	// --fd on the command line itself is taken too, and --args may not nest.
	_, plan, err = planDBusProxy([]string{"xdg-dbus-proxy", "--fd=6", "unix:path=/a", "/b"}, read)
	if err != nil || plan == nil || plan.syncFD != 6 || plan.socket != "/b" {
		t.Fatalf("plain: %+v %v", plan, err)
	}
	if _, _, err := planDBusProxy([]string{"xdg-dbus-proxy", "--args=7"}, func(int) ([]string, error) { return []string{"--args=8"}, nil }); err == nil {
		t.Fatal("nested --args accepted")
	}
}

func TestPlanDBusProxyLeavesOtherCommandsAlone(t *testing.T) {
	for _, argv := range [][]string{{"/usr/bin/gimp", "--args=7"}, {"sh", "-c", "xdg-dbus-proxy"}, nil} {
		got, plan, err := planDBusProxy(argv, func(int) ([]string, error) { t.Fatal("read"); return nil, nil })
		if err != nil || plan != nil || !reflect.DeepEqual(got, argv) {
			t.Errorf("%q: %q %v %v", argv, got, plan, err)
		}
	}
	for _, bad := range []string{"--args=x", "--args=1", "--fd=-1"} {
		if _, _, err := planDBusProxy([]string{"/usr/bin/xdg-dbus-proxy", bad}, func(int) ([]string, error) { return nil, nil }); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

// The proxy listens: flatpak's read end gets one byte. The app ends (the read
// end closes): the proxy is stopped.
func TestDBusProxyPipeHandshake(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "proxy.sock")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	p := &dbusProxy{syncFD: int(w.Fd()), socket: sock}
	var stopped atomic.Bool
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() { p.serve(done, func() { stopped.Store(true) }); close(finished) }()

	time.Sleep(100 * time.Millisecond) // nothing is listening yet: nothing written
	r.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if n, _ := r.Read(make([]byte, 1)); n != 0 {
		t.Fatal("told flatpak the proxy was ready before it listened")
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	r.SetReadDeadline(time.Now().Add(3 * time.Second))
	b := make([]byte, 1)
	if n, err := r.Read(b); n != 1 || b[0] != 'x' {
		t.Fatalf("no ready byte: %d %v", n, err)
	}
	if stopped.Load() {
		t.Fatal("stopped while the reader is still there")
	}
	r.Close() // the app ended
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("the proxy was not stopped when the reader went away")
	}
	if !stopped.Load() {
		t.Fatal("stop was not called")
	}
	close(done)
}

// The proxy dies before it listens: flatpak must see end-of-file, not a byte.
func TestDBusProxyThatNeverListensGivesFlatpakEOF(t *testing.T) {
	r, w, _ := os.Pipe()
	defer r.Close()
	p := &dbusProxy{syncFD: int(w.Fd()), socket: filepath.Join(t.TempDir(), "never.sock")}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() { p.serve(done, func() { t.Error("stop") }); close(finished) }()
	close(done)
	<-finished
	r.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, _ := r.Read(make([]byte, 1)); n != 0 {
		t.Fatal("a byte was sent although the proxy never listened")
	}
}
