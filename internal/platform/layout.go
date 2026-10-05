// Package platform describes ThothDock's on-disk layout and host facts.
package platform

import (
	"fmt"
	"os"
	"path/filepath"
)

// Layout is ThothDock's data root. Everything the daemon writes lives below
// it, owner-only.
type Layout struct {
	Root string
}

func (l Layout) Run() string        { return filepath.Join(l.Root, "run") }
func (l Layout) Socket() string     { return filepath.Join(l.Run(), "thothdock.sock") }
func (l Layout) LockFile() string   { return filepath.Join(l.Run(), "thothdock.lock") }
func (l Layout) Blobs() string      { return filepath.Join(l.Root, "blobs", "sha256") }
func (l Layout) Images() string     { return filepath.Join(l.Root, "images") }
func (l Layout) Containers() string { return filepath.Join(l.Root, "containers") }
func (l Layout) Tmp() string        { return filepath.Join(l.Root, "tmp") }
func (l Layout) Volumes() string    { return filepath.Join(l.Root, "volumes") }

// Ensure creates the layout with owner-only permissions and tightens the
// root if it already existed with wider ones.
func (l Layout) Ensure() error {
	if !filepath.IsAbs(l.Root) {
		return fmt.Errorf("data root %q is not an absolute path", l.Root)
	}
	for _, d := range []string{l.Root, l.Run(), l.Blobs(), l.Images(), l.Containers(), l.Tmp(), l.Volumes()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return os.Chmod(l.Root, 0o700)
}

// DefaultRoot is $THOTHDOCK_ROOT, else $XDG_DATA_HOME/thothdock, else
// $HOME/.local/share/thothdock.
func DefaultRoot() (string, error) {
	if r := os.Getenv("THOTHDOCK_ROOT"); r != "" {
		return filepath.Abs(r)
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "thothdock"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no data root: set THOTHDOCK_ROOT or --root")
	}
	return filepath.Join(home, ".local", "share", "thothdock"), nil
}
