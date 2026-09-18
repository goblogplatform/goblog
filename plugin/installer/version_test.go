package installer

import "testing"

func TestCompatible(t *testing.T) {
	cases := []struct {
		running, min string
		want         bool
	}{
		{"v0.2.7", "0.2.6", true},
		{"v0.2.7", "0.2.7", true},
		{"v0.2.7", "0.2.8", false},
		{"v0.2.7", "1.0.0", false},
		{"v1.0.0", "0.9.9", true},
		{"development", "9.9.9", true},
		{"latest", "9.9.9", true},
		{"", "9.9.9", true},
		{"v0.2.7", "", true},
		{"v0.2.7", "garbage", true},
	}
	for _, c := range cases {
		if got := compatible(c.running, c.min); got != c.want {
			t.Errorf("compatible(%q, %q) = %v, want %v", c.running, c.min, got, c.want)
		}
	}
}

func TestNewer(t *testing.T) {
	cases := []struct {
		candidate, current string
		want               bool
	}{
		{"1.1.0", "1.0.0", true},
		{"1.0.0", "1.0.0", false},
		{"1.0.0", "1.1.0", false},
		{"2.0.0", "1.9.9", true},
		{"v1.0.1", "1.0.0", true},
		{"garbage", "1.0.0", false},
		{"1.0.0", "garbage", false},
	}
	for _, c := range cases {
		if got := newer(c.candidate, c.current); got != c.want {
			t.Errorf("newer(%q, %q) = %v, want %v", c.candidate, c.current, got, c.want)
		}
	}
}
