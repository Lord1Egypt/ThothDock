package engine

import (
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/Lord1Egypt/ThothDock/internal/errdefs"
	"github.com/Lord1Egypt/ThothDock/internal/securefs"
	"github.com/Lord1Egypt/ThothDock/internal/volume"
)

// forbiddenTargets cannot be replaced by a mount: ThothDock manages them, or
// they are the host's own views.
var forbiddenTargets = []string{"/proc", "/sys", "/dev", "/etc/hosts", "/etc/hostname", "/etc/resolv.conf"}

func checkTarget(dst string) (string, error) {
	if !strings.HasPrefix(dst, "/") || strings.ContainsAny(dst, "\x00\n") || path.Clean(dst) == "/" {
		return "", errdefs.Invalid("invalid mount target %q: must be an absolute path other than /", dst)
	}
	t := path.Clean(dst)
	for _, f := range forbiddenTargets {
		if t == f || strings.HasPrefix(t, f+"/") {
			return "", errdefs.Invalid("invalid mount target %q: %s is managed by ThothDock", dst, f)
		}
	}
	return t, nil
}

// mountSpec is one entry of HostConfig.Mounts (API v1.41).
type mountSpec struct {
	Type          string
	Source        string
	Target        string
	ReadOnly      bool
	BindOptions   json.RawMessage
	VolumeOptions *struct {
		NoCopy       bool
		Labels       map[string]string
		DriverConfig *struct {
			Name    string
			Options map[string]string
		}
	}
	TmpfsOptions json.RawMessage
}

// resolveMounts turns -v / --mount into approved mounts. Named volumes are
// created on first use, as Docker does. Host paths still need --allow-bind.
func (e *Engine) resolveMounts(hc *HostConfig) ([]BindRecord, error) {
	var out []BindRecord
	seen := map[string]bool{}
	add := func(b BindRecord) error {
		if seen[b.Target] {
			return errdefs.Invalid("Duplicate mount point: %s", b.Target)
		}
		seen[b.Target] = true
		out = append(out, b)
		return nil
	}
	for _, b := range hc.Binds {
		parts := strings.Split(b, ":")
		if len(parts) < 2 || len(parts) > 3 {
			return nil, errdefs.Invalid("invalid volume specification: %q (want SOURCE:TARGET[:OPTIONS])", b)
		}
		src, dst := parts[0], parts[1]
		var nocopy bool
		if len(parts) == 3 {
			for _, o := range strings.Split(parts[2], ",") {
				switch o {
				case "rw", "":
				case "nocopy":
					nocopy = true
				case "ro":
					return nil, unsupported("read-only mounts", "PRoot binds are always writable; ThothDock refuses rather than grant write access")
				default:
					return nil, unsupported("mount option "+o, "only rw and nocopy exist")
				}
			}
		}
		_ = nocopy
		target, err := checkTarget(dst)
		if err != nil {
			return nil, err
		}
		var rec BindRecord
		if strings.HasPrefix(src, "/") {
			rec, err = e.hostBind(src, target)
		} else {
			rec, err = e.volumeBind(src, target, nil)
		}
		if err != nil {
			return nil, err
		}
		if err := add(rec); err != nil {
			return nil, err
		}
	}
	for _, raw := range hc.Mounts {
		var m mountSpec
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, errdefs.Invalid("invalid mount: %v", err)
		}
		if m.ReadOnly {
			return nil, unsupported("read-only mounts", "PRoot binds are always writable; ThothDock refuses rather than grant write access")
		}
		target, err := checkTarget(m.Target)
		if err != nil {
			return nil, err
		}
		var rec BindRecord
		switch m.Type {
		case "volume":
			var labels map[string]string
			if m.VolumeOptions != nil {
				labels = m.VolumeOptions.Labels
				if dc := m.VolumeOptions.DriverConfig; dc != nil {
					if dc.Name != "" && dc.Name != "local" {
						return nil, errdefs.Invalid("volume driver %q is not available; only \"local\" exists", dc.Name)
					}
					if len(dc.Options) > 0 {
						return nil, unsupported("volume driver options", "ThothDock volumes are plain directories")
					}
				}
			}
			rec, err = e.volumeBind(m.Source, target, labels)
		case "bind":
			if !strings.HasPrefix(m.Source, "/") {
				return nil, errdefs.Invalid("invalid bind mount source %q: must be an absolute path", m.Source)
			}
			rec, err = e.hostBind(m.Source, target)
		case "tmpfs":
			return nil, unsupported("tmpfs mounts", "an unprivileged app cannot mount filesystems")
		default:
			return nil, errdefs.Invalid("mount type %q is not supported", m.Type)
		}
		if err != nil {
			return nil, err
		}
		if err := add(rec); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (e *Engine) volumeBind(name, target string, labels map[string]string) (BindRecord, error) {
	if name != "" {
		if err := volume.ValidName(name); err != nil {
			return BindRecord{}, errdefs.Invalid("invalid volume specification: %v", err)
		}
	}
	v, err := e.Volumes.Create(name, "", labels, nil)
	if err != nil {
		return BindRecord{}, err
	}
	return BindRecord{Source: e.Volumes.DataPath(v.Name), Target: target, Volume: v.Name}, nil
}

func (e *Engine) hostBind(src, target string) (BindRecord, error) {
	canon, err := filepath.EvalSymlinks(src)
	if err != nil {
		return BindRecord{}, errdefs.Invalid("bind source %q: %v", src, err)
	}
	if canon, err = filepath.Abs(canon); err != nil {
		return BindRecord{}, err
	}
	if !e.bindAllowed(canon) {
		return BindRecord{}, errdefs.Forbidden("bind source %q is outside the directories this daemon allows (thothdock serve --allow-bind)", src)
	}
	return BindRecord{Source: canon, Target: target}, nil
}

// VolumeUsers lists the containers (running or not) that mount a volume.
func (e *Engine) VolumeUsers(name string) []string {
	var ids []string
	for _, r := range e.List() {
		for _, b := range r.Binds {
			if b.Volume == name {
				ids = append(ids, r.ID)
				break
			}
		}
	}
	return ids
}

// bindSources are the host paths to bind for a container's mounts, with named
// volumes resolved against the store now (the data directory must still be a
// real directory, never a symlink).
func (e *Engine) bindSources(binds []BindRecord) ([]BindRecord, error) {
	out := make([]BindRecord, 0, len(binds))
	for _, b := range binds {
		if b.Volume != "" {
			if !e.Volumes.Has(b.Volume) {
				return nil, errdefs.NotFound("volume %s no longer exists", b.Volume)
			}
			p := e.Volumes.DataPath(b.Volume)
			fi, err := os.Lstat(p)
			if err != nil || !fi.IsDir() {
				return nil, errdefs.Invalid("volume %s has no data directory", b.Volume)
			}
			b.Source = p
		}
		out = append(out, b)
	}
	return out, nil
}

// prepareMountpoints creates each mount target inside the container's own
// root filesystem (through securefs, so a symlink planted by the image cannot
// redirect it): a directory for a directory source, an empty file otherwise.
func prepareMountpoints(rootfs string, binds []BindRecord) error {
	root, err := securefs.OpenRoot(rootfs)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, b := range binds {
		fi, err := os.Stat(b.Source)
		if err != nil {
			return errdefs.Invalid("mount source %q: %v", b.Source, err)
		}
		if fi.IsDir() {
			if err := root.MkdirAll(b.Target); err != nil {
				if errors.Is(err, unix.ENOTDIR) {
					return errdefs.Invalid("mount point %s exists in the image and is not a directory", b.Target)
				}
				return errdefs.Invalid("cannot create mount point %s: %v", b.Target, err)
			}
			continue
		}
		par, err := root.OpenParent(b.Target, true, nil)
		if err != nil {
			return errdefs.Invalid("cannot create mount point %s: %v", b.Target, err)
		}
		fd, err := unixOpenatCreate(par.FD, par.Name)
		par.Close()
		if err != nil {
			return errdefs.Invalid("cannot create mount point %s: %v", b.Target, err)
		}
		_ = fd
	}
	return nil
}
