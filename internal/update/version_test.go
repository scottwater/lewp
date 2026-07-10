package update

import "testing"

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		left  string
		right string
		want  int
	}{
		{left: "v0.2.0", right: "0.1.0", want: 1},
		{left: "0.2.0", right: "v0.2.0", want: 0},
		{left: "1.2.0", right: "1.10.0", want: -1},
		{left: "1.10.1", right: "1.10.0", want: 1},
		{left: "v1.0.0-dirty", right: "1.0.0", want: 0},
	}
	for _, tc := range cases {
		if got := compareVersions(tc.left, tc.right); got != tc.want {
			t.Fatalf("compareVersions(%q, %q) = %d, want %d", tc.left, tc.right, got, tc.want)
		}
	}
}

func TestDisplayVersion(t *testing.T) {
	if got := DisplayVersion("0.2.0"); got != "v0.2.0" {
		t.Fatalf("DisplayVersion without v = %q", got)
	}
	if got := DisplayVersion("v0.2.0"); got != "v0.2.0" {
		t.Fatalf("DisplayVersion with v = %q", got)
	}
}
