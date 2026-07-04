//go:build !windows

package workbench

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockFile acquires an exclusive advisory lock on the store's lock file.
// Returns the lock file (caller must call unlockFile when done).
// This serializes cross-process access to the session store.
func (s *Store) lockFile() (*os.File, error) {
	lockPath := filepath.Join(s.root, ".lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	return f, nil
}

// unlockFile releases the advisory lock and closes the file.
func unlockFile(f *os.File) {
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	f.Close()
}
