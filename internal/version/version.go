package version

import (
	"fmt"
	"runtime/debug"
	"strings"

	stdruntime "runtime"
)

// Version is the framework version, overridden at build time via -ldflags.
var Version = "dev"

// Info describes the running binary.
type Info struct {
	Version  string `json:"version"`
	Commit   string `json:"commit,omitempty"`
	Dirty    bool   `json:"dirty,omitempty"`
	Go       string `json:"go"`
	Platform string `json:"platform"`
}

// String renders the info as a single human-readable line.
func (i Info) String() string {
	s := fmt.Sprintf("migr %s", i.Version)
	if i.showCommit() {
		s += fmt.Sprintf(" (%s%s)", i.Commit, dirtySuffix(i.Dirty))
	}

	return s + fmt.Sprintf(" %s %s", i.Go, i.Platform)
}

// showCommit reports whether the commit adds information the version lacks.
func (i Info) showCommit() bool {
	return i.Commit != "" && !strings.HasPrefix(i.Version, i.Commit)
}

// Get resolves build information for the running binary.
func Get() Info {
	info := Info{
		Version:  Version,
		Go:       stdruntime.Version(),
		Platform: stdruntime.GOOS + "/" + stdruntime.GOARCH,
	}

	build, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}

	info.Commit, info.Dirty = vcsSettings(build.Settings)

	return info
}

// vcsSettings extracts the revision and dirty flag stamped in by the go tool.
func vcsSettings(settings []debug.BuildSetting) (commit string, dirty bool) {
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			commit = shortCommit(s.Value)
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}

	return commit, dirty
}

// shortCommit truncates a full commit hash to its conventional short form.
func shortCommit(full string) string {
	const shortLen = 7
	if len(full) <= shortLen {
		return full
	}

	return full[:shortLen]
}

// dirtySuffix returns the marker appended to a commit from a modified tree.
func dirtySuffix(dirty bool) string {
	if dirty {
		return "-dirty"
	}

	return ""
}
