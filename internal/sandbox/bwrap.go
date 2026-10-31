// Package sandbox reads bubblewrap (bwrap) command lines. Flatpak runs every
// app through bwrap, which needs mount namespaces that a PRoot guest cannot
// give it (docs/APP_STORES.md); the guest's bwrap shim parses the command line
// with this package and asks the engine to build the same file system view
// with a PRoot of its own.
//
// The parser accepts bwrap's option set as of bubblewrap 0.12 and fails on
// anything else: a command line it does not understand is refused, never run
// with part of its sandbox missing. Options that need kernel features PRoot
// cannot provide (namespaces, seccomp, capabilities, uid mapping) are parsed
// and listed in Spec.Ignored so the caller can report them.
package sandbox

import (
	"errors"
	"fmt"
	"strconv"
)

// OpKind is a file system operation, applied in command-line order.
type OpKind string

const (
	OpBind       OpKind = "bind"         // Src on Dest, writable
	OpROBind     OpKind = "ro-bind"      // Src on Dest, read-only
	OpDevBind    OpKind = "dev-bind"     // Src on Dest, device nodes usable
	OpBindFD     OpKind = "bind-fd"      // the directory open on FD on Dest
	OpROBindFD   OpKind = "ro-bind-fd"   // same, read-only
	OpRemountRO  OpKind = "remount-ro"   // Dest becomes read-only
	OpProc       OpKind = "proc"         // a procfs on Dest
	OpDev        OpKind = "dev"          // a minimal /dev on Dest
	OpTmpfs      OpKind = "tmpfs"        // an empty writable directory on Dest
	OpMqueue     OpKind = "mqueue"       // a POSIX message queue fs on Dest
	OpDir        OpKind = "dir"          // mkdir Dest
	OpFile       OpKind = "file"         // the contents of FD as the file Dest
	OpBindData   OpKind = "bind-data"    // the contents of FD on Dest, writable
	OpROBindData OpKind = "ro-bind-data" // the contents of FD on Dest, read-only
	OpSymlink    OpKind = "symlink"      // a symlink at Dest pointing to Src
	OpChmod      OpKind = "chmod"        // chmod Dest to Mode
	OpLockFile   OpKind = "lock-file"    // hold a lock on Dest while running
)

// Op is one file system operation.
type Op struct {
	Kind OpKind
	Src  string // host path, or the symlink target
	Dest string // path inside the sandbox
	FD   int    // descriptor for the *-fd and *-data kinds and file, else -1
	Try  bool   // the -try variants: a missing Src is skipped, not an error
	// Perms is the mode a preceding --perms set for this dir, file, data
	// or tmpfs operation, or -1 for bwrap's default.
	Perms int
	// Size is the byte limit a preceding --size set for a tmpfs, or 0.
	Size int64
	Mode int // OpChmod only
}

// EnvOp changes the environment, in command-line order.
type EnvOp struct {
	Clear bool // --clearenv: drop everything set so far
	Unset bool // --unsetenv Name
	Name  string
	Value string // --setenv Name Value
}

// Spec is a parsed bwrap command line.
type Spec struct {
	Ops      []Op
	Env      []EnvOp
	Chdir    string
	Hostname string
	Argv0    string   // --argv0, or "" for Argv[0]
	Argv     []string // the command and its arguments

	// Descriptors bwrap reads or writes, or -1.
	InfoFD       int // receives {"child-pid": N}
	JSONStatusFD int // receives the child-pid and exit-code JSON lines
	SyncFD       int // kept open in the child; its closing tells the caller the sandbox ended
	BlockFD      int // the command starts only after this descriptor is read to EOF
	SeccompFDs   []int

	DieWithParent bool
	NewSession    bool
	AsPID1        bool

	// Ignored lists the options that were parsed but cannot be honoured
	// under PRoot, each with its argument, in command-line order.
	Ignored []string
}

// ErrNoCommand is returned when the options are not followed by a command.
var ErrNoCommand = errors.New("bwrap: no command given")

// arity is the number of arguments each option takes.
var arity = map[string]int{
	"--args": 1, "--argv0": 1,
	"--unshare-all": 0, "--share-net": 0, "--unshare-user": 0, "--unshare-user-try": 0,
	"--unshare-ipc": 0, "--unshare-pid": 0, "--unshare-net": 0, "--unshare-uts": 0,
	"--unshare-cgroup": 0, "--unshare-cgroup-try": 0, "--disable-userns": 0, "--assert-userns-disabled": 0,
	"--userns": 1, "--userns2": 1, "--pidns": 1, "--uid": 1, "--gid": 1,
	"--hostname": 1, "--chdir": 1, "--clearenv": 0, "--setenv": 2, "--unsetenv": 1,
	"--lock-file": 1, "--sync-fd": 1, "--perms": 1, "--size": 1, "--chmod": 2,
	"--bind": 2, "--bind-try": 2, "--dev-bind": 2, "--dev-bind-try": 2, "--ro-bind": 2, "--ro-bind-try": 2,
	"--bind-fd": 2, "--ro-bind-fd": 2, "--remount-ro": 1,
	"--exec-label": 1, "--file-label": 1,
	"--proc": 1, "--dev": 1, "--tmpfs": 1, "--mqueue": 1, "--dir": 1,
	"--file": 2, "--bind-data": 2, "--ro-bind-data": 2, "--symlink": 2,
	"--seccomp": 1, "--add-seccomp-fd": 1, "--block-fd": 1, "--userns-block-fd": 1,
	"--info-fd": 1, "--json-status-fd": 1,
	"--new-session": 0, "--die-with-parent": 0, "--as-pid-1": 0,
	"--cap-add": 1, "--cap-drop": 1, "--level-prefix": 0, "--not-a-security-boundary": 0,
	"--overlay-src": 1, "--overlay": 3, "--tmp-overlay": 1, "--ro-overlay": 1,
}

// maxArgsDepth bounds --args inside --args.
const maxArgsDepth = 8

// Parse reads a bwrap command line (without the program name). readArgs
// returns the NUL-separated arguments stored in an --args descriptor.
func Parse(args []string, readArgs func(fd int) ([]string, error)) (*Spec, error) {
	s := &Spec{InfoFD: -1, JSONStatusFD: -1, SyncFD: -1, BlockFD: -1}
	p := parser{spec: s, readArgs: readArgs, perms: -1}
	if err := p.parse(args, 0); err != nil {
		return nil, err
	}
	if p.perms != -1 || p.size != 0 {
		return nil, errors.New("bwrap: --perms or --size must be followed by the operation it applies to")
	}
	if len(s.Argv) == 0 {
		return nil, ErrNoCommand
	}
	return s, nil
}

type parser struct {
	spec     *Spec
	readArgs func(fd int) ([]string, error)
	perms    int   // pending --perms, -1 if none
	size     int64 // pending --size, 0 if none
}

func (p *parser) parse(args []string, depth int) error {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			p.spec.Argv = append([]string(nil), args[i+1:]...)
			return nil
		}
		n, ok := arity[a]
		if !ok {
			if len(a) > 1 && a[0] == '-' {
				return fmt.Errorf("bwrap: unknown option %q", a)
			}
			p.spec.Argv = append([]string(nil), args[i:]...)
			return nil
		}
		if i+n >= len(args) {
			return fmt.Errorf("bwrap: %s needs %d argument(s)", a, n)
		}
		v := args[i+1 : i+1+n]
		i += n
		if a == "--args" {
			if depth >= maxArgsDepth {
				return errors.New("bwrap: --args nested too deeply")
			}
			fd, err := parseFD(a, v[0])
			if err != nil {
				return err
			}
			if p.readArgs == nil {
				return errors.New("bwrap: --args given but descriptors cannot be read")
			}
			more, err := p.readArgs(fd)
			if err != nil {
				return fmt.Errorf("bwrap: --args %d: %w", fd, err)
			}
			if err := p.parse(more, depth+1); err != nil {
				return err
			}
			if len(p.spec.Argv) > 0 {
				return errors.New("bwrap: the command must follow the options, not sit inside --args")
			}
			continue
		}
		if err := p.option(a, v); err != nil {
			return err
		}
	}
	return nil
}

// takesPerms are the options a preceding --perms applies to, as in bwrap.
var takesPerms = map[string]bool{
	"--bind-data": true, "--ro-bind-data": true, "--dir": true, "--file": true, "--tmpfs": true,
}

func (p *parser) option(a string, v []string) error {
	// --perms and --size may be given together, in either order, before a --tmpfs.
	if p.perms != -1 && !takesPerms[a] && a != "--size" {
		return fmt.Errorf("bwrap: --perms must be followed by --bind-data, --ro-bind-data, --dir, --file or --tmpfs, not %s", a)
	}
	if p.size != 0 && a != "--tmpfs" && a != "--perms" {
		return fmt.Errorf("bwrap: --size must be followed by --tmpfs, not %s", a)
	}
	s := p.spec
	switch a {
	case "--argv0":
		s.Argv0 = v[0]
	case "--chdir":
		s.Chdir = v[0]
	case "--hostname":
		s.Hostname = v[0]
	case "--clearenv":
		s.Env = append(s.Env, EnvOp{Clear: true})
	case "--setenv":
		s.Env = append(s.Env, EnvOp{Name: v[0], Value: v[1]})
	case "--unsetenv":
		s.Env = append(s.Env, EnvOp{Unset: true, Name: v[0]})
	case "--perms":
		m, err := strconv.ParseUint(v[0], 8, 32)
		if err != nil || m > 0o7777 {
			return fmt.Errorf("bwrap: --perms %q is not an octal mode", v[0])
		}
		p.perms = int(m)
	case "--size":
		n, err := strconv.ParseInt(v[0], 10, 64)
		if err != nil || n <= 0 {
			return fmt.Errorf("bwrap: --size %q is not a positive byte count", v[0])
		}
		p.size = n
	case "--chmod":
		m, err := strconv.ParseUint(v[0], 8, 32)
		if err != nil || m > 0o7777 {
			return fmt.Errorf("bwrap: --chmod %q is not an octal mode", v[0])
		}
		p.add(Op{Kind: OpChmod, Dest: v[1], FD: -1, Mode: int(m)})
	case "--bind", "--bind-try":
		p.add(Op{Kind: OpBind, Src: v[0], Dest: v[1], Try: a == "--bind-try", FD: -1})
	case "--ro-bind", "--ro-bind-try":
		p.add(Op{Kind: OpROBind, Src: v[0], Dest: v[1], Try: a == "--ro-bind-try", FD: -1})
	case "--dev-bind", "--dev-bind-try":
		p.add(Op{Kind: OpDevBind, Src: v[0], Dest: v[1], Try: a == "--dev-bind-try", FD: -1})
	case "--bind-fd", "--ro-bind-fd":
		fd, err := parseFD(a, v[0])
		if err != nil {
			return err
		}
		k := OpBindFD
		if a == "--ro-bind-fd" {
			k = OpROBindFD
		}
		p.add(Op{Kind: k, FD: fd, Dest: v[1]})
	case "--remount-ro":
		p.add(Op{Kind: OpRemountRO, Dest: v[0], FD: -1})
	case "--proc":
		p.add(Op{Kind: OpProc, Dest: v[0], FD: -1})
	case "--dev":
		p.add(Op{Kind: OpDev, Dest: v[0], FD: -1})
	case "--mqueue":
		p.add(Op{Kind: OpMqueue, Dest: v[0], FD: -1})
	case "--lock-file":
		p.add(Op{Kind: OpLockFile, Dest: v[0], FD: -1})
	case "--tmpfs":
		p.addSized(Op{Kind: OpTmpfs, Dest: v[0], FD: -1})
	case "--dir":
		p.addSized(Op{Kind: OpDir, Dest: v[0], FD: -1})
	case "--symlink":
		p.add(Op{Kind: OpSymlink, Src: v[0], Dest: v[1], FD: -1})
	case "--file", "--bind-data", "--ro-bind-data":
		fd, err := parseFD(a, v[0])
		if err != nil {
			return err
		}
		k := map[string]OpKind{"--file": OpFile, "--bind-data": OpBindData, "--ro-bind-data": OpROBindData}[a]
		p.addSized(Op{Kind: k, FD: fd, Dest: v[1]})
	case "--info-fd", "--json-status-fd", "--sync-fd", "--block-fd", "--seccomp", "--add-seccomp-fd":
		fd, err := parseFD(a, v[0])
		if err != nil {
			return err
		}
		switch a {
		case "--info-fd":
			s.InfoFD = fd
		case "--json-status-fd":
			s.JSONStatusFD = fd
		case "--sync-fd":
			s.SyncFD = fd
		case "--block-fd":
			s.BlockFD = fd
		default:
			s.SeccompFDs = append(s.SeccompFDs, fd)
			s.Ignored = append(s.Ignored, a+" "+v[0])
		}
	case "--die-with-parent":
		s.DieWithParent = true
	case "--new-session":
		s.NewSession = true
	case "--as-pid-1":
		s.AsPID1 = true
	case "--level-prefix", "--not-a-security-boundary":
	case "--overlay-src", "--overlay", "--tmp-overlay", "--ro-overlay":
		return fmt.Errorf("bwrap: %s is not supported: PRoot cannot provide overlayfs", a)
	default:
		// Namespaces, uid/gid mapping, capabilities and SELinux labels.
		ig := a
		for _, x := range v {
			ig += " " + x
		}
		s.Ignored = append(s.Ignored, ig)
	}
	return nil
}

// add appends an operation that --perms and --size do not apply to.
func (p *parser) add(op Op) {
	op.Perms = -1
	p.spec.Ops = append(p.spec.Ops, op)
}

// addSized appends an operation that takes a pending --perms (and, for a
// tmpfs, --size), as bwrap does.
func (p *parser) addSized(op Op) {
	op.Perms = p.perms
	p.perms = -1
	if op.Kind == OpTmpfs {
		op.Size = p.size
		p.size = 0
	}
	p.spec.Ops = append(p.spec.Ops, op)
}

func parseFD(opt, v string) (int, error) {
	fd, err := strconv.Atoi(v)
	if err != nil || fd < 0 {
		return -1, fmt.Errorf("bwrap: %s %q is not a file descriptor", opt, v)
	}
	return fd, nil
}
