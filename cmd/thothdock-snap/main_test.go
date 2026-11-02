package main

import (
	"os/exec"
	"strings"
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

// Services start when asked: a window starts only what the user enabled with
// start --enable, and a service the snap switched off stays off.
func TestAutostartStartsOnlyEnabledServices(t *testing.T) {
	rec := &installed{Daemons: []string{"mosquitto", "worker"}}
	if autostarts(rec, "mosquitto") {
		t.Fatal("a service nobody enabled would start with every window")
	}
	rec.setSwitched("mosquitto", false) // start --enable
	if !autostarts(rec, "mosquitto") || autostarts(rec, "worker") {
		t.Fatalf("enable reached the wrong service: %+v", rec)
	}
	rec.setSwitched("mosquitto", true) // stop --disable
	if autostarts(rec, "mosquitto") || len(rec.Autostart) != 0 || len(rec.Disabled) != 1 {
		t.Fatalf("disable did not switch it off: %+v", rec)
	}
	rec.setSwitched("mosquitto", false)
	rec.Disabled = []string{"mosquitto"} // a snap that disabled it again behind our back
	if autostarts(rec, "mosquitto") {
		t.Fatal("a disabled service started")
	}
}

func TestEnableTwiceKeepsOneEntry(t *testing.T) {
	rec := &installed{}
	rec.setSwitched("b", false)
	rec.setSwitched("a", false)
	rec.setSwitched("b", false)
	if got := strings.Join(rec.Autostart, ","); got != "a,b" {
		t.Fatalf("autostart %q, want a,b", got)
	}
}

func TestPlural(t *testing.T) {
	if plural(1, "service") != "1 service" || plural(3, "service") != "3 services" {
		t.Fatal("plural wrong")
	}
}
