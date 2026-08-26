package migr

import (
	stdruntime "runtime"
	"runtime/debug"
)

// Name is what this SDK reports itself as in the handshake, which the engine logs.
const Name = "@migr/sdk-go"

// module is this SDK's own import path, which is how it finds itself in the build's dependencies.
const module = "github.com/emrebdr/migr/sdk/go"

// unknownVersion is what a build that records no version for this module reports.
const unknownVersion = "(devel)"

// Version reports this SDK's module version, or that the build did not record one.
func Version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return unknownVersion
	}

	if found := versionIn(info); found != "" {
		return found
	}

	return unknownVersion
}

// versionIn finds this module among the build's dependencies, which is where a release names it.
func versionIn(info *debug.BuildInfo) string {
	if info.Main.Path == module && info.Main.Version != "" {
		return info.Main.Version
	}

	for _, dep := range info.Deps {
		if dep.Path == module {
			return replaced(dep).Version
		}
	}

	return ""
}

// replaced follows a replace directive, so a locally developed SDK reports what it was replaced by.
func replaced(dep *debug.Module) *debug.Module {
	if dep.Replace != nil {
		return dep.Replace
	}

	return dep
}

// LanguageRuntime reports the Go toolchain this migration was compiled by, for the handshake.
func LanguageRuntime() string {
	return stdruntime.Version()
}

// Language is what the engine calls this runtime, spelled as migr.yaml spells migrations.language.
const Language = "go"
