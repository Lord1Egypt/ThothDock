package main

import "testing"

// doctor and serve share --guest-root; doctor used to define its own and
// panicked ("flag redefined") on every run once serve gained it.
func TestDoctorParsesGuestRootWithoutPanicking(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("doctor panicked: %v", r)
		}
	}()
	// An empty root has no Engine Guard: an error is expected, a panic is not.
	_ = doctor([]string{"--guard", "--guest-root", t.TempDir()})
}
