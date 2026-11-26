//go:build !linux && !darwin && !windows

package sitelock

import "os"

func platformTryLock(*os.File) error { return errUnsupported }

func platformUnlock(*os.File) error { return nil }
