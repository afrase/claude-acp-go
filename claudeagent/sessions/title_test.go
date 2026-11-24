package sessions

import (
	"strings"
	"testing"
)

// A title is on one line, its white space one space, and cut to
// MaxListTitle characters, counted as characters, not bytes.
func TestListTitle(t *testing.T) {
	long := strings.Repeat("é", 300)
	for _, tc := range []struct{ in, want string }{
		{"Fix  the\nlogin page ", "Fix the login page"},
		{long, strings.Repeat("é", 255) + "…"},
		{strings.Repeat("a", 256), strings.Repeat("a", 256)},
	} {
		if got := ListTitle(tc.in); got != tc.want {
			t.Errorf("ListTitle(%.20q) = %.20q…", tc.in, got)
		}
	}
}
