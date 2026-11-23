package buildinfo

import (
	"strings"
	"testing"
)

func TestGet(t *testing.T) {
	tests := []struct {
		name            string
		version, commit string
		wantVersion     string
		wantCommit      string // "" = anything non-empty (VCS fallback or unknown)
	}{
		{"stamped", "v1.2.3", "abc1234", "v1.2.3", "abc1234"},
		{"unstamped", Unknown, Unknown, Unknown, ""},
		{"empty version", "", "abc1234", Unknown, "abc1234"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldV, oldC := Version, Commit
			t.Cleanup(func() { Version, Commit = oldV, oldC })
			Version, Commit = tc.version, tc.commit

			got := Get()
			if got.Version != tc.wantVersion {
				t.Errorf("Version = %q, want %q", got.Version, tc.wantVersion)
			}
			if tc.wantCommit != "" && got.Commit != tc.wantCommit {
				t.Errorf("Commit = %q, want %q", got.Commit, tc.wantCommit)
			}
			if got.Commit == "" || !strings.HasPrefix(got.GoVersion, "go") {
				t.Errorf("incomplete info: %+v", got)
			}
			s := got.String()
			if !strings.Contains(s, got.Version) || !strings.Contains(s, "commit "+got.Commit) {
				t.Errorf("String() = %q", s)
			}
		})
	}
}
