package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/api"
	"github.com/Lord1Egypt/ThothDock/internal/engine"
	"github.com/Lord1Egypt/ThothDock/internal/image"
	"github.com/Lord1Egypt/ThothDock/internal/platform"
	"github.com/Lord1Egypt/ThothDock/internal/registry"
	"github.com/Lord1Egypt/ThothDock/internal/runtime"
	"github.com/Lord1Egypt/ThothDock/internal/securefs"
	"github.com/Lord1Egypt/ThothDock/internal/store"
	"github.com/Lord1Egypt/ThothDock/internal/version"
)

func newLogger(debug bool) *slog.Logger {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func daemonID(l platform.Layout) (string, error) {
	p := filepath.Join(l.Root, "engine-id")
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) == 36 {
		return strings.TrimSpace(string(b)), nil
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	h := hex.EncodeToString(b[:])
	id := h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
	return id, store.WriteFileAtomic(p, []byte(id+"\n"), 0o600)
}

// clearTmp removes staging leftovers of an interrupted daemon. Only called
// with the root lock held.
func clearTmp(l platform.Layout) error {
	entries, err := os.ReadDir(l.Tmp())
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := securefs.RemoveTree(filepath.Join(l.Tmp(), e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func loopbackOnly(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("--dev-tcp must be a loopback address (127.0.0.1:PORT or [::1]:PORT), got %q", addr)
	}
	return nil
}

type stack struct {
	layout  platform.Layout
	engine  *engine.Engine
	runtime *runtime.PRootRuntime
}

func build(o *options, log *slog.Logger) (*stack, error) {
	l, err := o.layout()
	if err != nil {
		return nil, err
	}
	rcfg, err := o.runtimeConfig(l)
	if err != nil {
		return nil, err
	}
	rt, err := runtime.NewPRoot(rcfg)
	if err != nil {
		return nil, err
	}
	blobs := store.NewBlobs(l.Blobs(), l.Tmp())
	images, err := image.Open(l.Images(), filepath.Join(l.Root, "refs.json"), l.Tmp(), blobs, log)
	if err != nil {
		return nil, err
	}
	puller := &image.Puller{Store: images, Client: registry.NewClient(registry.DefaultHTTPClient(), "ThothDock/"+version.Version), Limits: o.limits}
	var roots []string
	for _, b := range o.allowBind {
		c, err := filepath.EvalSymlinks(b)
		if err != nil {
			return nil, fmt.Errorf("--allow-bind %s: %w", b, err)
		}
		if c, err = filepath.Abs(c); err != nil {
			return nil, err
		}
		roots = append(roots, c)
	}
	eng, err := engine.New(l, images, puller, rt, engine.Config{AllowedBindRoots: roots, ResolvConf: o.resolvConf, AllowNonLoopbackPublish: o.allowPublish}, log)
	if err != nil {
		return nil, err
	}
	return &stack{layout: l, engine: eng, runtime: rt}, nil
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	var o options
	o.register(fs)
	shutdownTimeout := fs.Int("shutdown-timeout", 10, "seconds containers get to stop when the daemon exits")
	exitWithParent := fs.Bool("exit-with-parent", false, "shut down when the parent process (the Android app) dies")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if o.devTCP != "" {
		if err := loopbackOnly(o.devTCP); err != nil {
			return err
		}
	}
	log := newLogger(o.debug)
	if *exitWithParent {
		if err := exitWhenParentDies(); err != nil {
			return err
		}
	}
	// Android apps start with SIGHUP ignored, and an ignored disposition
	// survives exec. Handling it here gives every container process the
	// default disposition, as under dockerd.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			log.Debug("SIGHUP ignored by the daemon")
		}
	}()
	l, err := o.layout()
	if err != nil {
		return err
	}
	if err := l.Ensure(); err != nil {
		return err
	}
	lock, err := lockRoot(l)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := writePidFile(l); err != nil {
		return err
	}
	defer os.Remove(pidFile(l))
	if err := clearTmp(l); err != nil {
		return err
	}
	useResolver(o.resolvConf)
	st, err := build(&o, log)
	if err != nil {
		return err
	}
	id, err := daemonID(l)
	if err != nil {
		return err
	}
	rcfg := st.runtime.Config()
	srv := &api.Server{Engine: st.engine, Log: log, DaemonID: id, RuntimeName: "proot",
		RuntimePath: rcfg.Path, RuntimeVersion: prootVersion(st.runtime), Started: time.Now()}
	handler := srv.Handler()

	httpSrv := &http.Server{Handler: handler, ReadHeaderTimeout: 30 * time.Second}
	errc := make(chan error, 2)
	sock := o.socketPath(l)
	if o.socket == "none" {
		// Only for environments that may not create socket files (adb's
		// shell domain on Android); the production transport is the socket.
		if o.devTCP == "" {
			return errors.New("--socket none needs --dev-tcp")
		}
		sock = "(none)"
	} else {
		ln, created, err := listenUnix(sock)
		if err != nil {
			return err
		}
		defer removeOwnSocket(sock, created)
		go func() { errc <- httpSrv.Serve(ln) }()
	}
	if o.devTCP != "" {
		tl, err := net.Listen("tcp", o.devTCP)
		if err != nil {
			return err
		}
		log.Warn("DEVELOPMENT TCP listener enabled: unauthenticated Docker API on loopback", "addr", tl.Addr().String())
		go func() { errc <- httpSrv.Serve(tl) }()
	}
	log.Info("ThothDock listening", "version", version.Version, "api", version.APIVersion, "socket", sock,
		"root", l.Root, "runtime", rcfg.Path, "link2symlink", rcfg.LinkToSymlink)
	if o.socket != "none" {
		fmt.Fprintf(os.Stderr, "\n  export DOCKER_HOST=unix://%s\n\n", sock)
	}
	// Containers whose restart policy asks for it start again now that the
	// API answers (ADR-0004).
	st.engine.RestoreRestartPolicies()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
		}
	}
	log.Info("shutting down")
	stop()
	// A second SIGINT/SIGTERM, or a shutdown that overruns its budget,
	// ends the daemon at once; containers die with it (PDEATHSIG).
	again := make(chan os.Signal, 1)
	signal.Notify(again, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpSrv.Shutdown(sctx)
		st.engine.Shutdown(*shutdownTimeout)
		close(done)
	}()
	select {
	case <-done:
	case <-again:
		log.Warn("second signal: exiting without waiting for containers")
		return errors.New("interrupted during shutdown")
	case <-time.After(time.Duration(*shutdownTimeout+5) * time.Second):
		log.Warn("shutdown overran its budget: exiting")
		return errors.New("shutdown timed out")
	}
	log.Info("stopped")
	return nil
}
