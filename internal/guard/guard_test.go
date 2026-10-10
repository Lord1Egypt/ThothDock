package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, status string, files ...string) string {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "var/lib/dpkg"), 0o755)
	os.WriteFile(filepath.Join(root, "var/lib/dpkg/status"), []byte(status), 0o644)
	for _, f := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o755)
		os.WriteFile(filepath.Join(root, f), nil, 0o755)
	}
	return root
}

func rec(name, version, status string) string {
	return "Package: " + name + "\nStatus: " + status + "\nVersion: " + version + "\nDescription: x\n\n"
}

func find(fs []Finding, what string) Finding {
	for _, f := range fs {
		if f.What == what {
			return f
		}
	}
	return Finding{Level: -1}
}

func TestProtectedState(t *testing.T) {
	var st strings.Builder
	for _, p := range Protected {
		st.WriteString(rec(p, "9999:1.0+thothdock.1", "install ok installed"))
	}
	st.WriteString(rec("thothdock-engine-guard", "1.0+thothdock.1", "install ok installed"))
	fs := Check(fixture(t, st.String(), "usr/bin/docker", "usr/lib/thothdock/engine-guard-hook",
		"etc/apt/apt.conf.d/99thothdock-engine-guard", "etc/apt/preferences.d/thothdock-engine-guard"))
	if Worst(fs) != OK {
		t.Fatalf("%+v", fs)
	}
	if find(fs, "dockerd").Detail != "absent" {
		t.Fatal("dockerd should be absent")
	}
}

func TestRealEngineAndBinariesFail(t *testing.T) {
	st := rec("docker.io", "26.1.5+dfsg1-9+deb13u1", "install ok installed") + rec("runc", "9999:1.0+thothdock.1", "install ok installed")
	fs := Check(fixture(t, st, "usr/sbin/dockerd", "usr/bin/runc"))
	if find(fs, "docker.io").Level != Fail || find(fs, "dockerd").Level != Fail || find(fs, "runc").Detail == "" {
		t.Fatalf("%+v", fs)
	}
	if find(fs, "containerd").Level != Warn {
		t.Fatal("missing placeholder should warn")
	}
	// A removed (config-files-only) real package does not count as installed.
	fs = Check(fixture(t, rec("docker.io", "26.1.5", "deinstall ok config-files")))
	if find(fs, "docker.io").Level != Warn {
		t.Fatalf("%+v", find(fs, "docker.io"))
	}
}

func TestNotApplicableWithoutDpkg(t *testing.T) {
	fs := Check(t.TempDir())
	if len(fs) != 1 || fs[0].Level != OK || !strings.Contains(fs[0].Detail, "not applicable") {
		t.Fatalf("%+v", fs)
	}
}

func TestEnforcementFilesAreChecked(t *testing.T) {
	root := fixture(t, "", "usr/lib/thothdock/engine-guard-hook", "etc/apt/preferences.d/thothdock-engine-guard")
	fs := Check(root)
	if find(fs, "apt hook").Level != OK || find(fs, "apt pin").Level != OK {
		t.Fatalf("present files should pass: %+v", fs)
	}
	if f := find(fs, "apt hook configuration"); f.Level != Warn || !strings.Contains(f.Detail, "restored when a terminal window opens") {
		t.Fatalf("a missing configuration should warn: %+v", f)
	}
	os.Chmod(filepath.Join(root, "usr/lib/thothdock/engine-guard-hook"), 0o644)
	if find(Check(root), "apt hook").Level != Fail {
		t.Fatal("a hook that cannot run makes apt fail closed: that is a failure")
	}
}
