//go:build windows

package sitelock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// Windows byte-range locks are mandatory, so lock a byte far past the pid
// text and leave the pid readable to a process that fails to lock.
const lockOffsetHigh = 0x7FFFFFFF

func platformTryLock(f *os.File) error {
	ol := &windows.Overlapped{OffsetHigh: lockOffsetHigh}
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return errLocked
	case errors.Is(err, windows.ERROR_NOT_SUPPORTED), errors.Is(err, windows.ERROR_INVALID_FUNCTION):
		return errUnsupported
	}
	return err
}

func platformUnlock(f *os.File) error {
	ol := &windows.Overlapped{OffsetHigh: lockOffsetHigh}
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
}
