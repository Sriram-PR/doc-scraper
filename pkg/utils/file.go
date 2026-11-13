package utils

import (
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// RenameRetry is os.Rename that retries for a moment, absorbing transient
// Windows sharing violations: a reader, scanner or indexer holding the target
// open makes the rename fail with "Access is denied" until it closes it.
func RenameRetry(from, to string) error {
	var err error
	for i := range 5 {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		time.Sleep(time.Duration(i+1) * 50 * time.Millisecond)
	}
	return err
}

// WriteFileAtomic replaces path with data so that readers, and the file after
// a crash or power loss, see either the old contents or the new, never a mix.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := RenameRetry(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// SyncDir flushes a directory's entries so renames and new files in it survive
// a power loss. Windows can't sync a directory handle, so there it does nothing.
func SyncDir(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
