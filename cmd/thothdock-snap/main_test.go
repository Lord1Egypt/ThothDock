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
