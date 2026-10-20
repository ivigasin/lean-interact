package leaninteract

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const lockPollInterval = 50 * time.Millisecond

// acquireFileLock takes an exclusive inter-process lock on path, creating the
// file and its parent directories if needed. timeout <= 0 waits forever (or
// until ctx is done). The returned function releases the lock.
func acquireFileLock(ctx context.Context, path string, timeout time.Duration) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		unlock, ok, err := tryLock(path)
		if err != nil {
			return nil, err
		}
		if ok {
			return unlock, nil
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return nil, fmt.Errorf("leaninteract: timed out after %s waiting for lock %s", timeout, path)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(lockPollInterval):
		}
	}
}
