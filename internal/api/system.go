package api

import (
	"bufio"
	"net/http"
	"os"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Lord1Egypt/ThothDock/internal/oci"
	"github.com/Lord1Egypt/ThothDock/internal/platform"
	"github.com/Lord1Egypt/ThothDock/internal/procguard"
	"github.com/Lord1Egypt/ThothDock/internal/version"
)

func (s *Server) ping(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-cache, no-store, must-revalidate")
	h.Set("Pragma", "no-cache")
	h.Set("Swarm", "inactive")
	h.Set("Content-Type", "text/plain; charset=utf-8")
	if r.Method == http.MethodHead {
		h.Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
		return
	}
	w.Write([]byte("OK"))
}

func kernelVersion() string {
	var u unix.Utsname
	if unix.Uname(&u) != nil {
		return ""
	}
	return unix.ByteSliceToString(u.Release[:])
}

func machine() string {
	var u unix.Utsname
	if unix.Uname(&u) != nil {
		return goruntime.GOARCH
	}
	return unix.ByteSliceToString(u.Machine[:])
}

func operatingSystem() string {
	if platform.IsAndroid() {
		return "Android"
	}
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return "Linux"
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return "Linux"
}

func memTotal() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "MemTotal:"); ok {
			kb, _ := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB")), 10, 64)
			return kb * 1024
		}
	}
	return 0
}

func (s *Server) version(w http.ResponseWriter, r *http.Request) {
	details := map[string]string{
		"ApiVersion": version.APIVersion, "MinAPIVersion": version.MinAPIVersion,
		"GitCommit": version.GitCommit, "GoVersion": goruntime.Version(),
		"Os": "linux", "Arch": goruntime.GOARCH, "KernelVersion": kernelVersion(),
		"BuildTime": version.BuildTime, "Experimental": "false",
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"Platform": map[string]string{"Name": "ThothDock " + version.Version + " (Docker Engine API " + version.APIVersion + " compatible; not affiliated with Docker, Inc.)"},
		"Components": []map[string]any{
			{"Name": "Engine", "Version": version.Version, "Details": details},
			{"Name": s.RuntimeName, "Version": s.RuntimeVersion, "Details": map[string]string{"Path": s.RuntimePath}},
		},
		"Version": version.Version, "ApiVersion": version.APIVersion, "MinAPIVersion": version.MinAPIVersion,
		"GitCommit": version.GitCommit, "GoVersion": goruntime.Version(), "Os": "linux", "Arch": goruntime.GOARCH,
		"KernelVersion": kernelVersion(), "BuildTime": version.BuildTime, "Experimental": false,
	})
}

// infoWarnings are the honest differences from Docker Engine on Linux;
// the Docker CLI prints them under `docker info`.
var infoWarnings = []string{
	"WARNING: ThothDock runs containers under PRoot (ptrace) in user space: there are no kernel namespaces, cgroups, capabilities or seccomp profiles, so containers are not a security boundary",
	"WARNING: No memory, CPU or PID limit support (no cgroups)",
	"WARNING: Containers share the device network and process table (no network or PID namespace)",
	"WARNING: Root inside a container is emulated by PRoot and grants no host privilege",
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	running, stopped, total := s.Engine.Counts()
	hostname, _ := os.Hostname()
	plat := oci.HostPlatform()
	writeJSON(w, http.StatusOK, map[string]any{
		"ID": s.DaemonID, "Containers": total, "ContainersRunning": running, "ContainersPaused": 0, "ContainersStopped": stopped,
		"Images": s.Engine.Images.Count(), "Driver": "thothdock-copy",
		"DriverStatus":  [][2]string{{"Backing", "private copy of the image root filesystem per container (no OverlayFS)"}, {"Platform", plat.String()}},
		"DockerRootDir": s.Engine.Layout.Root,
		"Plugins":       map[string]any{"Volume": []string{}, "Network": []string{}, "Authorization": nil, "Log": []string{"json-file"}},
		"MemoryLimit":   false, "SwapLimit": false, "KernelMemory": false, "KernelMemoryTCP": false,
		"CpuCfsPeriod": false, "CpuCfsQuota": false, "CPUShares": false, "CPUSet": false, "PidsLimit": false,
		"OomKillDisable": false, "IPv4Forwarding": false, "BridgeNfIptables": false, "BridgeNfIp6tables": false,
		"Debug": false, "NFd": countFDs(), "NGoroutines": goruntime.NumGoroutine(), "SystemTime": time.Now().Format(time.RFC3339Nano),
		"LoggingDriver": "json-file", "CgroupDriver": "none", "CgroupVersion": "", "NEventsListener": 0,
		"KernelVersion": kernelVersion(), "OperatingSystem": operatingSystem(), "OSVersion": "", "OSType": "linux",
		"Architecture": machine(), "NCPU": goruntime.NumCPU(), "MemTotal": memTotal(),
		"IndexServerAddress": "https://index.docker.io/v1/",
		"RegistryConfig":     map[string]any{"AllowNondistributableArtifactsCIDRs": []string{}, "AllowNondistributableArtifactsHostnames": []string{}, "InsecureRegistryCIDRs": []string{}, "IndexConfigs": map[string]any{}, "Mirrors": []string{}},
		"GenericResources":   nil, "HttpProxy": "", "HttpsProxy": "", "NoProxy": "",
		"Name": hostname, "Labels": []string{}, "ExperimentalBuild": false, "ServerVersion": version.Version,
		"Runtimes":           map[string]any{s.RuntimeName: map[string]string{"path": s.RuntimePath}},
		"DefaultRuntime":     s.RuntimeName,
		"Swarm":              map[string]any{"NodeID": "", "NodeAddr": "", "LocalNodeState": "inactive", "ControlAvailable": false, "Error": "", "RemoteManagers": nil},
		"LiveRestoreEnabled": false, "Isolation": "", "InitBinary": "",
		"ContainerdCommit": map[string]string{"ID": "", "Expected": ""},
		"RuncCommit":       map[string]string{"ID": "", "Expected": ""},
		"InitCommit":       map[string]string{"ID": "", "Expected": ""},
		"SecurityOptions":  []string{},
		"Warnings":         s.infoWarnings(),
	})
}

// infoWarnings adds the live process-pressure warning, when there is one, to
// the fixed differences from Docker Engine.
func (s *Server) infoWarnings() []string {
	w := append([]string{}, infoWarnings...)
	if m := s.Engine.Pressure(); m != nil {
		if msg := procguard.Message(m.Recent(procguard.MaxAge), s.Engine.PendingCount()); msg != "" {
			w = append(w, "WARNING: "+msg)
		}
	}
	return w
}

// guardView is the JSON of GET /thothdock/process-guard, which the Android
// app and the Web Panel read. It is a ThothDock extension, not Docker API.
type guardView struct {
	Level      string  `json:"level"`
	Count      *int    `json:"count"`
	Cap        *int    `json:"cap"`
	CapSource  string  `json:"capSource"`
	ObservedAt *string `json:"observedAt"`
	AgeSeconds *int    `json:"ageSeconds"`
	Pending    int     `json:"pendingContainers"`
	Message    string  `json:"message"`
	Reason     string  `json:"unknownReason,omitempty"`
}

func (s *Server) processGuard(w http.ResponseWriter, r *http.Request) {
	v := guardView{Level: "unknown", CapSource: "Android's default (AOSP max_phantom_processes); this device's value cannot be read by an app", Pending: s.Engine.PendingCount()}
	snap := s.Engine.Pressure().Recent(procguard.MaxAge)
	v.Level = snap.Level.String()
	if snap.Level == procguard.Unknown {
		v.Reason = snap.Err
	} else {
		at := snap.At.UTC().Format(time.RFC3339)
		age := int(time.Since(snap.At).Seconds())
		v.Count, v.Cap, v.ObservedAt, v.AgeSeconds = &snap.Count, &snap.Cap, &at, &age
		v.Message = procguard.Message(snap, v.Pending)
	}
	writeJSON(w, http.StatusOK, map[string]any{"guard": v, "recovery": s.Engine.Recovery()})
}

func countFDs() int {
	es, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0
	}
	return len(es)
}
