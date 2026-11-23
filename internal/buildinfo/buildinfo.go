// Package buildinfo exposes the version and commit the binary was built from.
//
// Version and Commit are stamped at build time with
//
//	-ldflags "-X github.com/sheshakanthra/sluice/internal/buildinfo.Version=v0.1.0
//	          -X github.com/sheshakanthra/sluice/internal/buildinfo.Commit=abc1234"
//
// When they are not stamped (go run, plain go build), Commit falls back to the
// VCS revision Go embeds in builds made inside a git checkout, and finally to
// "unknown".
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Unknown is reported for any field that was not stamped and cannot be derived.
const Unknown = "unknown"

// Set via -ldflags -X. They must stay plain string variables for -X to work.
var (
	Version = Unknown
	Commit  = Unknown
)

// Info is the resolved build information.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	GoVersion string `json:"go_version"`
}

// Get resolves build information, using the embedded VCS revision when the
// commit was not stamped via -ldflags.
func Get() Info {
	info := Info{Version: Version, Commit: Commit, GoVersion: runtime.Version()}
	if info.Commit == Unknown || info.Commit == "" {
		info.Commit = vcsRevision()
	}
	if info.Version == "" {
		info.Version = Unknown
	}
	return info
}

// String renders the one-line form printed by `sluice version`.
func (i Info) String() string {
	return fmt.Sprintf("sluice %s (commit %s, %s)", i.Version, i.Commit, i.GoVersion)
}

func vcsRevision() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return Unknown
	}
	var rev, modified string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if rev == "" {
		return Unknown
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if modified == "true" {
		rev += "-dirty"
	}
	return rev
}
