package runlock

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gofrs/flock"
)

var ErrBusy = errors.New("lock is already held")

type FileLock struct {
	Path          string
	Name          string
	RetryInterval time.Duration
	Immediate     bool
}

func (lock FileLock) Lock(ctx context.Context) (func() error, error) {
	fileLock := flock.New(lock.Path)
	var (
		locked bool
		err    error
	)
	if lock.Immediate {
		locked, err = fileLock.TryLock()
	} else {
		interval := lock.RetryInterval
		if interval <= 0 {
			interval = 25 * time.Millisecond
		}
		locked, err = fileLock.TryLockContext(ctx, interval)
	}
	if err != nil {
		return nil, fmt.Errorf("acquire %s lock: %w", lock.Name, err)
	}
	if !locked {
		if lock.Immediate {
			return nil, fmt.Errorf("acquire %s lock: %w", lock.Name, ErrBusy)
		}
		return nil, errors.New(lock.Name + " lock wait ended without acquiring the lock")
	}
	return fileLock.Unlock, nil
}
