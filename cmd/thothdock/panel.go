package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/panel"
	"github.com/Lord1Egypt/ThothDock/internal/platform"
	"github.com/Lord1Egypt/ThothDock/internal/store"
	"github.com/Lord1Egypt/ThothDock/internal/version"
)

const panelPort = "7690"

// panelCmd runs the ThothDock Web Panel (docs/nextgen/adr/ADR-0005).
func panelCmd(args []string) error {
	fs := flag.NewFlagSet("panel", flag.ContinueOnError)
	def, _ := platform.DefaultRoot()
	root := fs.String("root", def, "the engine's data root (env THOTHDOCK_ROOT)")
	socket := fs.String("socket", "", "the engine's API socket (default <root>/run/thothdock.sock)")
	listen := fs.String("listen", "127.0.0.1:"+panelPort, "HTTPS address; any address but loopback exposes the panel to that network")
	stateDir := fs.String("state", "", "certificate and sessions (default <root>/panel)")
	revoke := fs.Bool("revoke-all", false, "end every paired browser session, then exit")
	exitWithParent := fs.Bool("exit-with-parent", false, "stop when the parent process exits (for the Android app)")
	debug := fs.Bool("debug", false, "log more")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *root == "" {
		return fmt.Errorf("no data root: pass --root or set THOTHDOCK_ROOT")
	}
	l := platform.Layout{Root: *root}
	if *socket == "" {
		*socket = l.Socket()
	}
	if *stateDir == "" {
		*stateDir = filepath.Join(l.Root, "panel")
	}
	if *revoke {
		if err := os.MkdirAll(*stateDir, 0o700); err != nil {
			return err
		}
		if err := panel.RevokeAll(filepath.Join(*stateDir, "sessions.json")); err != nil {
			return err
		}
		fmt.Println("All Web Panel sessions ended; browsers must pair again.")
		return nil
	}
	host, port, err := net.SplitHostPort(*listen)
	if err != nil {
		return fmt.Errorf("--listen: %v", err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("--listen: %q is not an IP address", host)
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("--listen: invalid port %q", port)
	}
	log := newLogger(*debug)
	if *exitWithParent {
		if err := exitWhenParentDies(); err != nil {
			return err
		}
	}
	srv, err := panel.New(panel.Config{Socket: *socket, StateDir: *stateDir, DataRoot: l.Root, Listen: *listen, Version: version.Version}, log)
	if err != nil {
		return err
	}
	if !ip.IsLoopback() {
		log.Warn("the Web Panel is reachable from the network: anyone there can try to pair; keep the pairing code private", "listen", *listen)
	}
	urls := []string{"https://" + net.JoinHostPort(host, port) + "/"}
	if ip.IsUnspecified() {
		urls = nil
		addrs, _ := net.InterfaceAddrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLinkLocalUnicast() {
				urls = append(urls, "https://"+net.JoinHostPort(n.IP.String(), port)+"/")
			}
		}
	}
	pairingFile := filepath.Join(*stateDir, "pairing.json")
	announce := func() error {
		code, expires, err := srv.NewCode()
		if err != nil {
			return err
		}
		fmt.Printf("ThothDock Web Panel\n")
		for _, u := range urls {
			fmt.Printf("  URL:           %s\n", u)
		}
		fmt.Printf("  Certificate:   SHA-256 %s\n", srv.Fingerprint())
		fmt.Printf("  Pairing code:  %s %s (valid until %s)\n\n", code[:4], code[4:], expires.Format("15:04"))
		return store.WriteJSONAtomic(pairingFile, map[string]any{"urls": urls, "fingerprint": srv.Fingerprint(),
			"code": code, "expires": expires.UTC().Format(time.RFC3339)})
	}
	if err := announce(); err != nil {
		return err
	}
	defer os.Remove(pairingFile)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	usr1 := make(chan os.Signal, 1)
	signal.Notify(usr1, syscall.SIGUSR1)
	go func() {
		for range usr1 {
			if err := announce(); err != nil {
				log.Error("new pairing code", "err", err)
			}
		}
	}()
	return srv.Run(ctx)
}
