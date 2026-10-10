package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/layer"
	"github.com/Lord1Egypt/ThothDock/internal/logs"
	"github.com/Lord1Egypt/ThothDock/internal/oci"
	"github.com/Lord1Egypt/ThothDock/internal/securefs"
	"github.com/Lord1Egypt/ThothDock/internal/store"
)

func unsupported(feature, why string) error {
	return errdefs.Unsupported("ThothDock does not support %s: %s (see docs/COMPATIBILITY.md)", feature, why)
}

const noCgroups = "containers run under PRoot without cgroups, so the limit would not be enforced"

// validateHostConfig refuses what PRoot cannot honour instead of silently
// pretending, and returns warnings for what it honours differently.
func (e *Engine) validateHostConfig(hc *HostConfig) ([]string, error) {
	var warnings []string
	if err := RefuseResourceLimits(hc); err != nil {
		return nil, err
	}
	switch {
	case hc.Privileged:
		return nil, unsupported("privileged containers", "PRoot cannot grant kernel privileges")
	case hc.CgroupParent != "":
		return nil, unsupported("cgroup parents", "there are no cgroups")
	case len(hc.CapAdd) > 0 || len(hc.CapDrop) > 0:
		return nil, unsupported("capability changes", "PRoot processes run with the Android app's (empty) capability set")
	case len(hc.Devices) > 0 || len(hc.DeviceRequests) > 0:
		return nil, unsupported("device passthrough", "an unprivileged app cannot create or grant device nodes")
	case len(hc.VolumesFrom) > 0:
		return nil, unsupported("--volumes-from", "not implemented yet")
	case len(hc.Links) > 0:
		return nil, unsupported("container links", "there is no container network")
	case hc.ReadonlyRootfs:
		return nil, unsupported("read-only root filesystems", "PRoot cannot enforce read-only mounts")
	case len(hc.Tmpfs) > 0:
		return nil, unsupported("tmpfs mounts", "an unprivileged app cannot mount filesystems")
	case len(hc.Sysctls) > 0:
		return nil, unsupported("sysctls", "there is no network or IPC namespace to tune")
	case len(hc.SecurityOpt) > 0:
		return nil, unsupported("security options", "no seccomp, AppArmor or SELinux profiles can be applied")
	case hc.Init != nil && *hc.Init:
		return nil, unsupported("--init", "not implemented yet")
	case hc.OomKillDisable != nil && *hc.OomKillDisable:
		return nil, unsupported("--oom-kill-disable", noCgroups)
	case hc.ShmSize > 0:
		return nil, unsupported("--shm-size", "/dev/shm is the device's own")
	case len(hc.GroupAdd) > 0:
		return nil, unsupported("--group-add", "not implemented yet")
	}
	if err := validateRestartPolicy(hc.RestartPolicy, hc.AutoRemove); err != nil {
		return nil, err
	}
	if hc.Runtime != "" && hc.Runtime != e.Runtime.Name() {
		return nil, errdefs.Invalid("unknown runtime %q (ThothDock has %q)", hc.Runtime, e.Runtime.Name())
	}
	if hc.LogConfig.Type != "" && hc.LogConfig.Type != "json-file" {
		return nil, unsupported("log driver "+hc.LogConfig.Type, "only json-file exists")
	}
	if len(hc.LogConfig.Config) > 0 {
		warnings = append(warnings, "log options are ignored; logs rotate at the daemon's limit")
	}
	for _, m := range []struct{ name, val string }{{"PID", hc.PidMode}, {"IPC", hc.IpcMode}, {"UTS", hc.UTSMode}, {"user namespace", hc.UsernsMode}} {
		if m.val != "" && m.val != "host" && !(m.name == "IPC" && (m.val == "private" || m.val == "shareable")) {
			return nil, unsupported(m.name+" mode "+m.val, "there are no namespaces; every container shares the device's")
		}
	}
	switch hc.NetworkMode {
	case "bridge":
		warnings = append(warnings, "the default bridge is the device network (like --network host); create a network for per-container addresses and names")
	case "none":
		return nil, unsupported("--network none", "there is no network namespace to isolate the container in")
	}
	return warnings, nil
}

// RefuseResourceLimits refuses any cgroup-backed limit (create and docker
// update): there are no cgroups to enforce it, so it is never accepted and
// silently ignored.
func RefuseResourceLimits(hc *HostConfig) error {
	switch {
	case hc.Memory > 0, hc.MemoryReservation > 0, hc.MemorySwap > 0:
		return unsupported("memory limits", noCgroups)
	case hc.NanoCpus > 0, hc.CpuQuota > 0, hc.CpuPeriod > 0, hc.CpuShares > 0, hc.CpusetCpus != "", hc.CpusetMems != "":
		return unsupported("CPU limits", noCgroups)
	case hc.PidsLimit != nil && *hc.PidsLimit > 0:
		return unsupported("PID limits", noCgroups)
	case hc.BlkioWeight > 0 || len(hc.BlkioDeviceReadBps) > 0:
		return unsupported("block I/O limits", noCgroups)
	case len(hc.Ulimits) > 0:
		return unsupported("ulimits", "not implemented yet")
	}
	return nil
}

// validateRestartPolicy applies dockerd's rules for HostConfig.RestartPolicy.
func validateRestartPolicy(p RestartPolicy, autoRemove bool) error {
	switch p.Name {
	case "", PolicyNo, PolicyAlways, PolicyUnlessStopped:
		if p.MaximumRetryCount != 0 {
			return errdefs.Invalid("maximum retry count cannot be used with restart policy '%s'", p.Name)
		}
	case PolicyOnFailure:
		if p.MaximumRetryCount < 0 {
			return errdefs.Invalid("invalid restart policy: maximum retry count cannot be negative")
		}
	default:
		return errdefs.Invalid("invalid restart policy '%s'", p.Name)
	}
	if autoRemove && p.Name != "" && p.Name != PolicyNo {
		return errdefs.Invalid("can't create 'AutoRemove' container with restart policy")
	}
	return nil
}

func (e *Engine) bindAllowed(canon string) bool {
	// Never ThothDock's own data, nor any directory that contains it.
	if strings.HasPrefix(canon+"/", e.Layout.Root+"/") || strings.HasPrefix(e.Layout.Root+"/", canon+"/") {
		return false
	}
	for _, root := range e.cfg.AllowedBindRoots {
		if canon == root || strings.HasPrefix(canon, root+"/") {
			return true
		}
	}
	return false
}

// Create makes a container from an image: validated configuration, a
// private copy of the image's root filesystem, its hosts and resolver
// files, and finally its persisted record.
func (e *Engine) Create(req CreateRequest, name, platform string) (string, []string, error) {
	if platform != "" {
		p, err := oci.ParsePlatform(platform)
		if err != nil {
			return "", nil, errdefs.Invalid("%v", err)
		}
		if !oci.Matches(oci.HostPlatform(), p) {
			return "", nil, errdefs.Invalid("platform %s cannot run on this %s device (no emulation)", p, oci.HostPlatform())
		}
	}
	if name != "" && !nameRe.MatchString(strings.TrimPrefix(name, "/")) {
		return "", nil, errdefs.Invalid("Invalid container name (%s), only [a-zA-Z0-9][a-zA-Z0-9_.-] are allowed", name)
	}
	name = strings.TrimPrefix(name, "/")
	if req.Image == "" {
		return "", nil, errdefs.Invalid("no image specified")
	}
	img, err := e.Images.Get(req.Image)
	if err != nil {
		return "", nil, err
	}
	hc := req.HostConfig
	if hc == nil {
		hc = &HostConfig{}
	}
	warnings, err := e.validateHostConfig(hc)
	if err != nil {
		return "", nil, err
	}
	if len(req.Healthcheck) > 0 && string(req.Healthcheck) != "null" {
		return "", nil, unsupported("health checks", "not implemented yet")
	}
	nets, err := e.resolveNetworks(hc.NetworkMode, req.NetworkingConfig)
	if err != nil {
		return "", nil, err
	}
	binds, err := e.resolveMounts(hc)
	if err != nil {
		return "", nil, err
	}
	exposed := req.ExposedPorts
	if exposed == nil {
		exposed = img.Config.Config.ExposedPorts
	}
	_, portWarnings, err := e.planPorts(hc, exposed, len(nets) > 0)
	if err != nil {
		return "", nil, err
	}
	warnings = append(warnings, portWarnings...)

	cfg := req.ContainerConfig
	ic := img.Config.Config
	cfg.Env = mergeEnv(ic.Env, cfg.Env)
	if !hasEnv(cfg.Env, "PATH") {
		cfg.Env = append([]string{"PATH=" + defaultPath}, cfg.Env...)
	}
	if cfg.Entrypoint == nil {
		cfg.Entrypoint = ic.Entrypoint
		if len(cfg.Cmd) == 0 {
			cfg.Cmd = ic.Cmd
		}
	} else if len(cfg.Entrypoint) == 1 && cfg.Entrypoint[0] == "" {
		cfg.Entrypoint = StrSlice{}
	}
	if cfg.WorkingDir == "" {
		cfg.WorkingDir = ic.WorkingDir
	}
	if cfg.WorkingDir == "" {
		cfg.WorkingDir = "/"
	}
	if !strings.HasPrefix(cfg.WorkingDir, "/") {
		return "", nil, errdefs.Invalid("the working directory '%s' is invalid, it needs to be an absolute path", cfg.WorkingDir)
	}
	if cfg.User == "" {
		cfg.User = ic.User
	}
	if cfg.StopSignal == "" {
		cfg.StopSignal = ic.StopSignal
	}
	if len(ic.Labels) > 0 {
		labels := map[string]string{}
		for k, v := range ic.Labels {
			labels[k] = v
		}
		for k, v := range cfg.Labels {
			labels[k] = v
		}
		cfg.Labels = labels
	}
	if len(ic.ExposedPorts) > 0 && cfg.ExposedPorts == nil {
		cfg.ExposedPorts = ic.ExposedPorts
	}
	for v := range ic.Volumes {
		warnings = append(warnings, fmt.Sprintf("image volume %s stays inside the container's filesystem (anonymous volumes are not implemented)", v))
	}
	for v := range cfg.Volumes {
		if _, ok := ic.Volumes[v]; !ok {
			warnings = append(warnings, fmt.Sprintf("volume %s stays inside the container's filesystem (anonymous volumes are not implemented)", v))
		}
	}
	argv := append(append([]string{}, cfg.Entrypoint...), cfg.Cmd...)
	if len(argv) == 0 {
		return "", nil, errdefs.Invalid("no command specified")
	}
	if cfg.Hostname != "" && !validHostname(cfg.Hostname) {
		return "", nil, errdefs.Invalid("invalid hostname %q", cfg.Hostname)
	}
	if cfg.Image == "" {
		cfg.Image = req.Image
	}

	e.mu.Lock()
	if name == "" {
		name = e.generateNameLocked()
	} else if id, taken := e.names[name]; taken {
		e.mu.Unlock()
		return "", nil, errdefs.Conflict("Conflict. The container name \"/%s\" is already in use by container \"%s\". You have to remove (or rename) that container to be able to reuse that name.", name, id)
	}
	id := newID()
	e.names[name] = id // reserved while the filesystem is prepared
	e.mu.Unlock()
	var netIP string
	if len(nets) > 0 {
		if netIP, err = e.addrs.Allocate(id); err != nil {
			e.mu.Lock()
			delete(e.names, name)
			e.mu.Unlock()
			return "", nil, err
		}
	} else {
		nets = nil
	}
	release := func() {
		e.mu.Lock()
		delete(e.names, name)
		e.mu.Unlock()
		if netIP != "" {
			e.addrs.Release(netIP)
		}
	}
	if cfg.Hostname == "" {
		cfg.Hostname = id[:12]
	}

	dir := filepath.Join(e.Layout.Containers(), id)
	rec := Record{ID: id, Name: name, Created: time.Now().UTC(), Image: img.ID, ImageRef: req.Image,
		Config: cfg, HostConfig: *hc, Path: argv[0], Args: argv[1:], Binds: binds,
		State: State{Status: StatusCreated}, Networks: nets, NetIP: netIP}
	moreWarnings, err := e.prepareDir(dir, &rec)
	if err != nil {
		release()
		securefs.RemoveTree(dir)
		return "", nil, err
	}
	warnings = append(warnings, moreWarnings...)
	if err := prepareMountpoints(filepath.Join(dir, "rootfs"), binds); err != nil {
		release()
		securefs.RemoveTree(dir)
		return "", nil, err
	}
	c := &Container{rec: rec, dir: dir, stdin: newStdinBroker()}
	if c.logger, err = logs.Open(filepath.Join(dir, "container.log"), e.cfg.LogMaxSize); err != nil {
		release()
		securefs.RemoveTree(dir)
		return "", nil, err
	}
	// The record is written last: a directory without it is an interrupted
	// create and is removed on the next start.
	if err := e.persist(c); err != nil {
		c.logger.Close()
		release()
		securefs.RemoveTree(dir)
		return "", nil, err
	}
	e.mu.Lock()
	e.containers[id] = c
	e.mu.Unlock()
	if netIP != "" {
		// Its own hosts file and its peers' now that it is registered.
		e.syncHosts(netSet(nets))
	}
	e.log.Info("container created", "id", id[:12], "name", name, "image", req.Image, "address", netIP)
	c.mu.Lock()
	e.containerEvent(c, "create")
	c.mu.Unlock()
	if warnings == nil {
		warnings = []string{}
	}
	return id, warnings, nil
}

func (e *Engine) prepareDir(dir string, rec *Record) ([]string, error) {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	rootfs := filepath.Join(dir, "rootfs")
	if _, err := layer.CopyTree(e.Images.RootfsPath(rec.Image), rootfs); err != nil {
		return nil, fmt.Errorf("preparing the container filesystem: %w", err)
	}
	root, err := securefs.OpenRoot(rootfs)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := root.MkdirAll(rec.Config.WorkingDir); err != nil {
		return nil, errdefs.Invalid("cannot create working directory %s: %v", rec.Config.WorkingDir, err)
	}
	var hosts []byte
	if rec.NetIP != "" {
		hosts, err = hostsFor(*rec, nil) // peers are added by syncHosts after registration
	} else {
		hosts, err = hostsFile(rec.Config.Hostname, rec.HostConfig.ExtraHosts)
	}
	if err != nil {
		return nil, err
	}
	resolv, warnings, err := resolvFile(e.cfg.ResolvConf, rec.HostConfig.Dns, rec.HostConfig.DnsSearch, rec.HostConfig.DnsOptions)
	if err != nil {
		return nil, err
	}
	for name, data := range map[string][]byte{"hosts": hosts, "resolv.conf": resolv, "hostname": []byte(rec.Config.Hostname + "\n")} {
		if err := store.WriteFileAtomic(filepath.Join(dir, name), data, 0o644); err != nil {
			return nil, err
		}
	}
	return warnings, nil
}
