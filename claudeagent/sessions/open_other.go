//go:build !unix

package sessions

import (
	"errors"
	"os"
)

// openRegular opens path for reading only when it is a regular file, not
// a link: there is no O_NOFOLLOW here, so it is checked first. The
// caller's Stat of what was opened still turns away anything else.
func openRegular(path string) (*os.File, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("sessions: not a regular file: " + path)
	}
	return os.Open(path)
}
