// Package version reports build information. Values are set at build time:
//
//	go build -ldflags "-X github.com/jackellisw/nzbfirstframe/internal/version.Version=v0.1.0"
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// String returns a one-line version description.
func String() string {
	commit, date := Commit, Date
	if commit == "" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, s := range info.Settings {
				switch s.Key {
				case "vcs.revision":
					commit = s.Value
				case "vcs.time":
					if date == "" {
						date = s.Value
					}
				}
			}
		}
	}
	if len(commit) > 12 {
		commit = commit[:12]
	}
	s := "nzbff " + Version
	if commit != "" {
		s += " (" + commit
		if date != "" {
			s += ", " + date
		}
		s += ")"
	}
	return fmt.Sprintf("%s %s/%s %s", s, runtime.GOOS, runtime.GOARCH, runtime.Version())
}
