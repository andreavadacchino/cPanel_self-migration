package migrate

import "testing"

// TestHasPathPrefixTrailingSlash guards the fix where a trailing slash on dir made a
// real descendant fail the boundary check.
func TestHasPathPrefixTrailingSlash(t *testing.T) {
	cases := []struct {
		path, dir string
		want      bool
	}{
		{"/home/u/public_html/wp-config.php", "/home/u/public_html/", true},
		{"/home/u/public_html/wp-config.php", "/home/u/public_html", true},
		{"/home/u/public_html", "/home/u/public_html/", true},
		{"/home/u/public_html/x", "/home/u/public_html//", true},
		{"/a/bc", "/a/b", false},
		{"/a/bc", "/a/b/", false},
		{"/home/u/x", "/home/u/public_html", false},
	}
	for _, c := range cases {
		if got := hasPathPrefix(c.path, c.dir); got != c.want {
			t.Errorf("hasPathPrefix(%q,%q)=%v want %v", c.path, c.dir, got, c.want)
		}
	}
}
