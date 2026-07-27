package version

import (
	"strings"
	"testing"
)

func TestInfoString(t *testing.T) {
	tests := []struct {
		name string
		info Info
		want string
	}{
		{
			name: "tagged release shows both version and commit",
			info: Info{Version: "v1.2.3", Commit: "abc1234", Go: "go1.26.3", Platform: "linux/amd64"},
			want: "migr v1.2.3 (abc1234) go1.26.3 linux/amd64",
		},
		{
			name: "untagged build does not repeat the commit",
			info: Info{Version: "abc1234-dirty", Commit: "abc1234", Dirty: true, Go: "go1.26.3", Platform: "linux/amd64"},
			want: "migr abc1234-dirty go1.26.3 linux/amd64",
		},
		{
			name: "plain go build reports dev plus the stamped commit",
			info: Info{Version: "dev", Commit: "abc1234", Go: "go1.26.3", Platform: "linux/amd64"},
			want: "migr dev (abc1234) go1.26.3 linux/amd64",
		},
		{
			name: "dirty marker appears when version omits it",
			info: Info{Version: "dev", Commit: "abc1234", Dirty: true, Go: "go1.26.3", Platform: "linux/amd64"},
			want: "migr dev (abc1234-dirty) go1.26.3 linux/amd64",
		},
		{
			name: "no vcs stamp at all",
			info: Info{Version: "v1.2.3", Go: "go1.26.3", Platform: "linux/amd64"},
			want: "migr v1.2.3 go1.26.3 linux/amd64",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.info.String(); got != tt.want {
				t.Errorf("String() =\n  %q\nwant\n  %q", got, tt.want)
			}
		})
	}
}

func TestShortCommit(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "4194b48f1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f", want: "4194b48"},
		{in: "abc", want: "abc"},
		{in: "", want: ""},
	}

	for _, tt := range tests {
		if got := shortCommit(tt.in); got != tt.want {
			t.Errorf("shortCommit(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestGetReportsRuntimeFacts(t *testing.T) {
	info := Get()

	if info.Version == "" {
		t.Error("Version must never be empty")
	}

	if !strings.HasPrefix(info.Go, "go") {
		t.Errorf("Go = %q, want a go version string", info.Go)
	}

	if !strings.Contains(info.Platform, "/") {
		t.Errorf("Platform = %q, want os/arch", info.Platform)
	}
}
