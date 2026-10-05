package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/guard"
)

// doctorGuard prints the Engine Guard report for the filesystem at root.
// standalone also reports the Docker CLI and the engine reached through
// $DOCKER_HOST, as run inside a guest.
func doctorGuard(root string, standalone bool) error {
	fmt.Println("Engine Guard")
	findings := guard.Check(root)
	for _, f := range findings {
		fmt.Printf("  %-4s  %-26s %s\n", f.Level, f.What, f.Detail)
	}
	if standalone {
		fmt.Println()
		fmt.Printf("  %-4s  %-26s %s\n", "INFO", "Docker CLI", dockerCLIVersion())
		eng, api := engineVersion()
		fmt.Printf("  %-4s  %-26s %s\n", "INFO", "ThothDock Engine", eng)
		fmt.Printf("  %-4s  %-26s %s\n", "INFO", "Docker API", api)
	}
	if guard.Worst(findings) == guard.Fail {
		return fmt.Errorf("Engine Guard: a stock engine is present")
	}
	return nil
}

func dockerCLIVersion() string {
	out, err := exec.Command("docker", "--version").Output()
	if err != nil {
		return "not found"
	}
	v := strings.TrimSpace(string(out))
	v = strings.TrimPrefix(v, "Docker version ")
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	return v
}

// engineVersion asks $DOCKER_HOST (a unix:// socket) for /version.
func engineVersion() (engine, api string) {
	host := os.Getenv("DOCKER_HOST")
	sock, ok := strings.CutPrefix(host, "unix://")
	if !ok {
		return "unreachable (DOCKER_HOST is not a unix:// socket)", "unknown"
	}
	c := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}}}
	resp, err := c.Get("http://thothdock/version")
	if err != nil {
		return "offline (" + err.Error() + ")", "unknown"
	}
	defer resp.Body.Close()
	var v struct{ Version, ApiVersion, MinAPIVersion string }
	if json.NewDecoder(resp.Body).Decode(&v) != nil {
		return "unreadable", "unknown"
	}
	return v.Version, v.ApiVersion + " (minimum " + v.MinAPIVersion + ")"
}
