//go:build linux || darwin

package sitelock

import (
	"errors"
	"os"
	"syscall"
)

func platformTryLock(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return errLocked
	case errors.Is(err, syscall.ENOLCK), errors.Is(err, syscall.ENOTSUP), errors.Is(err, syscall.EOPNOTSUPP):
		return errUnsupported
	}
	return err
}

func platformUnlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
