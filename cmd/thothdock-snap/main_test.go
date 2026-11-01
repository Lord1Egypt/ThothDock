package main

import (
	"os/exec"
	"testing"
)

func exitErr(code int) error {
	err := exec.Command("sh", "-c", "exit "+map[int]string{1: "1", 2: "2"}[code]).Run()
	return err
}

// core20 on the SM-A165F: only its /dev nodes fail, which the sandbox never uses.
func TestOnlyDeviceNodeErrorsAreAccepted(t *testing.T) {
	dev := []byte("set_attributes: failed to set time on /snap/core20/1.partial/dev/null, because No such file or directory\n" +
		"set_attributes: failed to change mode /snap/core20/1.partial/dev/zero, because No such file or directory\ncreated 108 hardlinks\n")
	if !onlyDeviceNodeErrors(exitErr(2), dev, "/snap/core20/1.partial") {
		t.Fatal("device-node-only failures refused")
	}
	other := append(dev, []byte("write_file: failed to write /snap/core20/1.partial/usr/lib/libc.so.6, because No space left on device\n")...)
	if onlyDeviceNodeErrors(exitErr(2), other, "/snap/core20/1.partial") {
		t.Fatal("a real failure was accepted")
	}
	if onlyDeviceNodeErrors(exitErr(1), dev, "/snap/core20/1.partial") {
		t.Fatal("exit 1 accepted")
	}
	if onlyDeviceNodeErrors(exitErr(2), []byte("created 3 files\n"), "/snap/core20/1.partial") {
		t.Fatal("exit 2 with no explained error accepted")
	}
}

func TestExpandVars(t *testing.T) {
	env := map[string]string{"SNAP": "/snap/m/1", "SNAP_LIBRARY_PATH": "/gl", "PATH": "/usr/bin"}
	get := func(k string) string { return env[k] }
	for in, want := range map[string]string{
		"${SNAP_LIBRARY_PATH}${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}:$SNAP/usr/lib": "/gl:/snap/m/1/usr/lib",
		"$SNAP/usr/bin:$PATH": "/snap/m/1/usr/bin:/usr/bin",
		"${NOPE:-fallback}":   "fallback",
		"${PATH:+has}":        "has",
		"cost $5 and $":       "cost $5 and $", // no positional parameters here
		"${unterminated":      "${unterminated",
	} {
		if got := expandVars(in, get); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	env["LD_LIBRARY_PATH"] = "/x"
	if got := expandVars("${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}", get); got != ":/x" {
		t.Errorf("set :+ got %q", got)
	}
}
