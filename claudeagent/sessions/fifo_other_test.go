//go:build !unix

package sessions

import "testing"

// mkfifo skips the rest of the test: there are no named pipes in a
// folder here.
func mkfifo(t *testing.T, _ string) {
	t.Helper()
	t.Skip("no named pipes outside Unix")
}
