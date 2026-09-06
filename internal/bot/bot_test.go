package bot

import (
	"reflect"
	"testing"
)

func TestSplitColon(t *testing.T) {
	cases := []struct{ in, a, b string }{
		{"approve:12345", "approve", "12345"},
		{"c:7", "c", "7"},
		{"mu:7:15", "mu", "7:15"},
		{"m", "m", ""},
	}
	for _, c := range cases {
		a, b := splitColon(c.in)
		if a != c.a || b != c.b {
			t.Errorf("splitColon(%q) = %q,%q want %q,%q", c.in, a, b, c.a, c.b)
		}
	}
}

func TestToggle(t *testing.T) {
	got := toggle([]string{"person", "car"}, "dog")
	if !reflect.DeepEqual(got, []string{"person", "car", "dog"}) {
		t.Fatalf("add: %v", got)
	}
	got = toggle([]string{"person", "car"}, "car")
	if !reflect.DeepEqual(got, []string{"person"}) {
		t.Fatalf("remove: %v", got)
	}
}

func TestValidStreamURL(t *testing.T) {
	for _, ok := range []string{"rtsp://h/1", "RTSP://h/1", "https://h/s.m3u8"} {
		if validStreamURL(ok) != nil {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"ftp://h", "h/1", "javascript:x"} {
		if validStreamURL(bad) == nil {
			t.Errorf("%q should be invalid", bad)
		}
	}
}
