package main

import (
	"reflect"
	"testing"
)

func TestParseArgs(t *testing.T) {
	spec := flagSpec{"output": true, "size": true, "dry-run": false, "y": false}
	alias := map[string]string{"o": "output"}
	flags, pos, err := parseArgs([]string{"a.mp4", "-o", "out.mp4", "b.mp4", "--size=720p", "--dry-run", "-y", "--", "-weird.mp4"}, spec, alias)
	if err != nil {
		t.Fatal(err)
	}
	wantFlags := map[string]string{"output": "out.mp4", "size": "720p", "dry-run": "true", "y": "true"}
	if !reflect.DeepEqual(flags, wantFlags) {
		t.Errorf("flags = %v", flags)
	}
	if want := []string{"a.mp4", "b.mp4", "-weird.mp4"}; !reflect.DeepEqual(pos, want) {
		t.Errorf("pos = %q", pos)
	}
	for _, bad := range [][]string{{"--nope"}, {"--size"}, {"--dry-run=1"}, {"-y", "-y"}} {
		if _, _, err := parseArgs(bad, spec, alias); err == nil {
			t.Errorf("parseArgs(%q) should fail", bad)
		}
	}
}

func TestFmtDur(t *testing.T) {
	for in, want := range map[float64]string{0: "0:00.0", 18.04: "0:18.0", 71.54: "1:11.5", 3725.3: "1:02:05.3"} {
		if got := fmtDur(in); got != want {
			t.Errorf("fmtDur(%v) = %s, want %s", in, got, want)
		}
	}
}
