package opts

import "testing"

func TestParseSize(t *testing.T) {
	for in, want := range map[string]Size{"1920x1080": {1920, 1080}, "1280X720": {1280, 720}, "4k": {3840, 2160}, "720p": {1280, 720}, "1080×1920": {1080, 1920}} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "1920", "1921x1080", "axb", "0x0", "99999x2"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) should fail", bad)
		}
	}
}

func TestParseFPS(t *testing.T) {
	for in, want := range map[string]string{"30": "30", "29.97": "30000/1001", "23.976": "24000/1001", "59.94": "60000/1001", "25": "25", "30000/1001": "30000/1001", "12.5": "12500/1000"} {
		if got, err := ParseFPS(in); err != nil || got.String() != want {
			t.Errorf("ParseFPS(%q) = %v, %v; want %s", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "0", "-5", "abc", "1/0", "1000"} {
		if _, err := ParseFPS(bad); err == nil {
			t.Errorf("ParseFPS(%q) should fail", bad)
		}
	}
}

func TestSnap(t *testing.T) {
	for in, want := range map[float64]string{29.87: "30000/1001", 30.02: "30", 24.0: "24", 0: "30", 59.2: "60000/1001", 25.1: "25"} {
		if got := Snap(in).String(); got != want {
			t.Errorf("Snap(%v) = %s, want %s", in, got, want)
		}
	}
}

func TestParseColor(t *testing.T) {
	for in, want := range map[string]string{"#000000": "#000000", "FFaa00": "#ffaa00", "#abc": "#aabbcc"} {
		if got, err := ParseColor(in); err != nil || got != want {
			t.Errorf("ParseColor(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"red", "#12345", "#gggggg", ""} {
		if _, err := ParseColor(bad); err == nil {
			t.Errorf("ParseColor(%q) should fail", bad)
		}
	}
}

func TestParsePreset(t *testing.T) {
	if p, err := ParsePreset("Fast"); err != nil || p != Fast {
		t.Error(p, err)
	}
	if _, err := ParsePreset("best"); err == nil {
		t.Error("expected error")
	}
}
