package runlock

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

type FileLock struct {
	Path          string
	Name          string
	RetryInterval time.Duration
}

type FileClaimer struct {
	Root          string
	Namespace     string
	RetryInterval time.Duration
}

func (claimer FileClaimer) Claim(ctx context.Context, key string) (func() error, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, errors.New("claim key is required")
	}
	if claimer.Root == "" {
		return nil, errors.New("claim root is required")
	}
	switch {
	case claimer.Namespace == "", claimer.Namespace == ".", claimer.Namespace == "..",
		filepath.Base(claimer.Namespace) != claimer.Namespace:
		return nil, errors.New("claim namespace must be one path segment")
	}
	digest := sha256.Sum256([]byte(key))
	encoded := fmt.Sprintf("%x", digest)
	directory := filepath.Join(
		claimer.Root, ".knowbrew", "state", claimer.Namespace, encoded[:2],
	)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, fmt.Errorf("create claim directory: %w", err)
	}
	lock := FileLock{
		Path:          filepath.Join(directory, encoded+".lock"),
		Name:          claimer.Namespace + " claim",
		RetryInterval: claimer.RetryInterval,
	}
	return lock.Lock(ctx)
}

func (lock FileLock) Lock(ctx context.Context) (func() error, error) {
	fileLock := flock.New(lock.Path)
	interval := lock.RetryInterval
	if interval <= 0 {
		interval = 25 * time.Millisecond
	}
	locked, err := fileLock.TryLockContext(ctx, interval)
	if err != nil {
		return nil, fmt.Errorf("acquire %s lock: %w", lock.Name, err)
	}
	if !locked {
		return nil, errors.New(lock.Name + " lock wait ended without acquiring the lock")
	}
	return fileLock.Unlock, nil
}
