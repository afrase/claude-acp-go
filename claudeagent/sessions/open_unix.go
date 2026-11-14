//go:build unix

package sessions

import (
	"os"
	"syscall"
)

// openRegular opens path for reading without following a link or
// waiting on a pipe; the caller still checks it is a regular file.
func openRegular(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
