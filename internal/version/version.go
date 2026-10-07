// Package version holds build identity, set at link time with -ldflags -X.
package version

var (
	// Version is the ThothDock release version.
	Version = "0.1.1"
	// GitCommit is the source commit the binary was built from.
	GitCommit = "unknown"
	// BuildTime is the RFC 3339 build timestamp, when known.
	BuildTime = ""
)

const (
	// APIVersion is the Docker Engine API version ThothDock implements and
	// advertises. Requests for a newer version are refused.
	APIVersion = "1.41"
	// MinAPIVersion is the oldest API version ThothDock accepts.
	MinAPIVersion = "1.24"
)
