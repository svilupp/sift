package cli

import (
	"errors"
	"fmt"

	"github.com/gofrs/flock"

	"sift/internal/config"
)

// errLockHeld is returned by acquireLock when the file lock is held by
// another process. Callers can use errors.Is to add context (e.g. point
// at a running daemon) before surfacing the failure to the user.
var errLockHeld = errors.New("sift operation lock is held")

// acquireLock tries to acquire the sift operation lock.
// Returns the lock if successful, or an error if the lock is held.
// The caller MUST defer Unlock() on the returned lock.
//
// When the lock is held by another process, the returned error wraps
// errLockHeld so callers can detect it via errors.Is and tailor the
// user-facing message (e.g. "daemon is running").
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
		return nil, fmt.Errorf("%w: another sift operation is running, try again later (lock: %s)", errLockHeld, lockPath)
	}

	return fl, nil
}
