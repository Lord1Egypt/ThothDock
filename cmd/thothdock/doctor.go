package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	goruntime "runtime"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Lord1Egypt/ThothDock/internal/runtime"
)

func doctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	var o options
	o.register(fs)
	guardOnly := fs.Bool("guard", false, "only the Engine Guard report (runs inside a guest)")
	guestRoot := fs.String("guest-root", "/", "root filesystem to inspect for the Engine Guard")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *guardOnly {
		return doctorGuard(*guestRoot, true)
	}
	failed := false
	report := func(status, what, detail string) {
		if status == "FAIL" {
			failed = true
		}
		fmt.Printf("%-4s  %-28s %s\n", status, what, detail)
	}
	var u unix.Utsname
	unix.Uname(&u)
	report("INFO", "kernel", unix.ByteSliceToString(u.Release[:])+" "+unix.ByteSliceToString(u.Machine[:]))
	report("INFO", "binary", goruntime.GOOS+"/"+goruntime.GOARCH+" "+goruntime.Version())
	if _, err := os.Stat("/system/build.prop"); err == nil {
		report("INFO", "platform", "Android")
	}
	l, err := o.layout()
	if err != nil {
		report("FAIL", "data root", err.Error())
		return fmt.Errorf("checks failed")
	}
	if err := l.Ensure(); err != nil {
		report("FAIL", "data root", err.Error())
	} else {
		fi, _ := os.Stat(l.Root)
		report("PASS", "data root", fmt.Sprintf("%s (%v)", l.Root, fi.Mode().Perm()))
	}
	var sfs unix.Statfs_t
	if unix.Statfs(l.Root, &sfs) == nil {
		free := int64(sfs.Bavail) * int64(sfs.Bsize)
		status := "PASS"
		if free < 1<<30 {
			status = "WARN"
		}
		report(status, "free space", fmt.Sprintf("%.1f GiB (each container is a full copy of its image)", float64(free)/(1<<30)))
	}
	if hardlinksWork(l.Tmp()) {
		report("PASS", "hard links", "allowed; --link2symlink=auto leaves them native")
	} else {
		report("WARN", "hard links", "refused (Android SELinux); containers use PRoot --link2symlink")
	}
	if f, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0); err == nil {
		f.Close()
		report("PASS", "pseudo-terminals", "/dev/ptmx usable (docker run -t)")
	} else {
		report("FAIL", "pseudo-terminals", err.Error())
	}
	rcfg, err := o.runtimeConfig(l)
	if err != nil {
		report("FAIL", "proot", err.Error())
	} else if rt, err := runtime.NewPRoot(rcfg); err != nil {
		report("FAIL", "proot", err.Error())
	} else {
		report("PASS", "proot", rcfg.Path+" — "+prootVersion(rt))
		// Run this very binary as a PRoot tracee against the host root: it
		// proves ptrace, the loader and the libraries all work here.
		self, _ := os.Executable()
		var out bytes.Buffer
		p, err := rt.Start(runtime.Spec{Rootfs: "/", Args: []string{self, "version"}, Cwd: "/", Stdout: &out, Stderr: &out})
		if err != nil {
			report("FAIL", "proot execution", err.Error())
		} else if ex := p.Wait(); ex.Code != 0 {
			report("FAIL", "proot execution", fmt.Sprintf("exit %d: %s", ex.Code, out.String()))
		} else {
			report("PASS", "proot execution", "a traced process ran and exited 0")
		}
	}
	if ns := nameservers(o.resolvConf); len(ns) > 0 {
		report("PASS", "DNS", fmt.Sprintf("%s: %v", o.resolvConf, ns))
	} else {
		report("WARN", "DNS", "no nameservers in "+o.resolvConf+"; pass --resolv-conf (containers fall back to 8.8.8.8)")
	}
	sock := o.socketPath(l)
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	if resp, err := client.Get("http://thothdock/_ping"); err == nil {
		resp.Body.Close()
		fi, _ := os.Stat(sock)
		report("PASS", "daemon", fmt.Sprintf("answering on %s (%v)", sock, fi.Mode().Perm()))
	} else {
		report("INFO", "daemon", "not running on "+sock)
	}
	if _, err := exec.LookPath("dockerd"); err == nil {
		report("INFO", "dockerd", "present on PATH but unused by ThothDock")
	}
	if *guestRoot != "/" {
		if err := doctorGuard(*guestRoot, false); err != nil {
			failed = true
		}
	}
	if failed {
		return fmt.Errorf("some checks failed")
	}
	return nil
}
