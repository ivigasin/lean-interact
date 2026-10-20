//go:build !unix

package leaninteract

import (
	"errors"
	"fmt"
	"os"
)

// tryLock falls back to an exclusively created marker file holding the owner
// PID. Unlike flock, a crashed owner leaves the file behind; delete it
// manually in that case.
func tryLock(path string) (func(), bool, error) {
	marker := path + ".pid"
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	fmt.Fprint(f, os.Getpid())
	f.Close()
	return func() { os.Remove(marker) }, true, nil
}
