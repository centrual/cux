package updater

import (
	"path/filepath"
	"testing"
	"time"
)

func TestIsNewer(t *testing.T) {
	cases := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{name: "patch", a: "0.2.1", b: "0.2.0", want: true},
		{name: "minor", a: "0.3.0", b: "0.2.9", want: true},
		{name: "major", a: "1.0.0", b: "0.9.9", want: true},
		{name: "same", a: "0.2.0", b: "0.2.0", want: false},
		{name: "older", a: "0.1.9", b: "0.2.0", want: false},
		{name: "strip suffix", a: "0.3.0+build.1", b: "0.2.0", want: true},
		{name: "short semver", a: "0.3", b: "0.2.9", want: true},
		{name: "fallback", a: "beta", b: "alpha", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsNewer(tc.a, tc.b); got != tc.want {
				t.Fatalf("IsNewer(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestResultHasUpdateStripsV(t *testing.T) {
	r := Result{Current: "v0.2.0", Latest: "v0.3.0"}
	if !r.HasUpdate() {
		t.Fatal("expected v0.3.0 to be newer than v0.2.0")
	}
}

func TestCachedResultDoesNotRequireNetwork(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, ".local", "share"))

	polled := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	if err := saveCache(Cache{
		Polled:  polled,
		Latest:  "v0.3.0",
		HTMLURL: "https://example.com/release",
	}); err != nil {
		t.Fatal(err)
	}

	r, ok := CachedResult("v0.2.5")
	if !ok {
		t.Fatal("expected cached result")
	}
	if !r.HasUpdate() {
		t.Fatalf("expected cached result to report update: %+v", r)
	}
	if r.Current != "0.2.5" || r.Latest != "0.3.0" || r.HTMLURL != "https://example.com/release" || !r.Polled.Equal(polled) {
		t.Fatalf("unexpected cached result: %+v", r)
	}
}
