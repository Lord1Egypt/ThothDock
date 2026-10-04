// Command thothdock is a Docker Engine API-compatible container engine for
// unrooted Android, running OCI images through Garden's PRoot userspace
// runtime instead of namespaces, cgroups and runc.
package main

import (
	"fmt"
	"os"

	"github.com/Lord1Egypt/ThothDock/internal/version"
)

const usage = `ThothDock — a Docker-compatible userspace container engine for Android.
Not affiliated with Docker, Inc.

Usage:
  thothdock serve [flags]        run the daemon (Docker Engine API on a Unix socket)
  thothdock version              print version information
  thothdock doctor [flags]       check this device and configuration
  thothdock inspect-store [-verify]  list images, blobs and containers
  thothdock gc                   remove unreferenced blobs and leftovers (daemon stopped)

Run "thothdock <command> -h" for a command's flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Printf("ThothDock %s (commit %s, Docker Engine API %s)\n", version.Version, version.GitCommit, version.APIVersion)
	case "doctor":
		err = doctor(os.Args[2:])
	case "inspect-store":
		err = inspectStore(os.Args[2:])
	case "gc":
		err = gc(os.Args[2:])
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "thothdock: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "thothdock:", err)
		os.Exit(1)
	}
}
