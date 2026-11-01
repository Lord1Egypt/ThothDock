// go-probe: Go runtime checks for F-GO (goroutines on many threads, os/exec,
// pipes, signals, files, DNS, HTTPS, an HTTP server and client). Built
// static (CGO_ENABLED=0) for the target architecture by probes/build.sh.
package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

var failures int

func check(ok bool, what string) {
	if ok {
		fmt.Println("PASS " + what)
	} else {
		fmt.Println("FAIL " + what)
		failures++
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		srv := &http.Server{Addr: ":8000", Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "go-ok") })}
		go srv.ListenAndServe()
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGTERM)
		<-c
		srv.Close()
		return
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	sum := 0
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := 0
			for j := 0; j < 1e6; j++ {
				s += j
			}
			mu.Lock()
			sum += s
			mu.Unlock()
		}()
	}
	wg.Wait()
	check(sum == 64*499999500000, "go: 64 goroutines")

	out, err := exec.Command("sh", "-c", "echo sub; exit 3").Output()
	ee, _ := err.(*exec.ExitError)
	check(string(out) == "sub\n" && ee != nil && ee.ExitCode() == 3, "go: os/exec output and exit code")
	cmd := exec.Command("tr", "a-z", "A-Z")
	cmd.Stdin = strings.NewReader("piped")
	up, _ := cmd.Output()
	check(string(up) == "PIPED", "go: pipes to a child")

	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGUSR1)
	syscall.Kill(os.Getpid(), syscall.SIGUSR1)
	select {
	case <-c:
		check(true, "go: signal.Notify")
	case <-time.After(2 * time.Second):
		check(false, "go: signal.Notify")
	}

	d, _ := os.MkdirTemp("", "g")
	f := filepath.Join(d, "f")
	os.WriteFile(f, make([]byte, 1<<20), 0o600)
	err = os.Rename(f, f+".2")
	st, _ := os.Stat(f + ".2")
	check(err == nil && st != nil && st.Size() == 1<<20, "go: write and rename 1 MiB")
	os.RemoveAll(d)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err == nil {
		go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "loop") }))
		r, err := http.Get("http://" + ln.Addr().String())
		body := ""
		if err == nil {
			b, _ := io.ReadAll(r.Body)
			body = string(b)
		}
		check(body == "loop", "go: HTTP server and client over loopback")
	} else {
		check(false, "go: listen on 127.0.0.1")
	}

	_, err = net.LookupHost("dns.google")
	check(err == nil, fmt.Sprintf("go: DNS lookup (%v)", err))
	cl := &http.Client{Timeout: 15 * time.Second}
	r, err := cl.Get("https://dns.google/resolve?name=example.com")
	ok := false
	if err == nil {
		b, _ := io.ReadAll(r.Body)
		ok = strings.Contains(string(b), "Answer")
	}
	check(ok, fmt.Sprintf("go: HTTPS request, certificate verified (%v)", err))
	fmt.Printf("go: %d failure(s)\n", failures)
	if failures > 0 {
		os.Exit(1)
	}
}
