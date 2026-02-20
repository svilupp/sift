package cli

import (
	"fmt"

	"github.com/gofrs/flock"

	"sift/internal/config"
)

// acquireLock tries to acquire the sift operation lock.
// Returns the lock if successful, or an error if the lock is held.
// The caller MUST defer Unlock() on the returned lock.
func acquireLock() (*flock.Flock, error) {
	lockPath, err := config.LockPath()
	if err != nil {
		return nil, err
	}

	fl := flock.New(lockPath)
	locked, err := fl.TryLock()
	if err != nil {
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	if !locked {
		return nil, fmt.Errorf("another sift operation is running, try again later (lock: %s)", lockPath)
	}

	return fl, nil
}
