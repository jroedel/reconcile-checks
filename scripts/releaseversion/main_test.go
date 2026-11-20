package main

import (
	"strings"
	"testing"
)

func TestTheNextVersion(t *testing.T) {
	cases := []struct {
		latest, labels, want string
	}{
		{"", "", "v0.1.0"},
		{"null", "release: minor", "v0.1.0"},
		{"v0.1.0", "", "v0.1.1"},
		{"v0.1.9", "bug", "v0.1.10"},
		{"v0.3.1", "release: minor,bug", "v0.4.0"},
		{"v0.3.1", "Release: Minor", "v0.4.0"},
		{"v0.3.1", "release: minor,release: major", "v1.0.0"},
		{"v1.2.3", "release: major", "v2.0.0"},
	}

	for _, c := range cases {
		got, err := next(c.latest, strings.Split(c.labels, ","))
		if err != nil || got != c.want {
			t.Errorf("next(%q, %q) = %q, %v; want %q", c.latest, c.labels, got, err, c.want)
		}
	}
}

func TestALatestThatIsNoVersionStopsTheRelease(t *testing.T) {
	for _, latest := range []string{"1.2.3", "v1.2", "v1.2.3-rc1", "v01.2.3", "untagged-abc"} {
		if got, err := next(latest, nil); err == nil {
			t.Errorf("next(%q) = %q, and no error", latest, got)
		}
	}
}
