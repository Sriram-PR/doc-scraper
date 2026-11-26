// Package sitelock keeps two crawls of one site from running at once, across
// processes and inside one. The OS releases a lock when its holder dies.
package sitelock

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
)

// ErrBusy matches a *BusyError.
var ErrBusy = errors.New("site is already being crawled")

// BusyError reports that another holder, possibly in this process, has the
// site's lock. PID is 0 when the holder's pid could not be read.
type BusyError struct {
	Site string
	PID  int
}

func (e *BusyError) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("site '%s' is already being crawled (pid %d)", e.Site, e.PID)
	}
	return fmt.Sprintf("site '%s' is already being crawled", e.Site)
}

func (e *BusyError) Is(target error) bool { return target == ErrBusy }

var (
	errLocked      = errors.New("lock held elsewhere")
	errUnsupported = errors.New("file locking unsupported")
)

// tryLockFile is swapped by tests.
var tryLockFile = platformTryLock

// Lock is a held site lock. A Lock from a filesystem without lock support
// holds nothing.
type Lock struct {
	site string
	mu   sync.Mutex
	f    *os.File
}

// TryLock takes siteKey's crawl lock without waiting. It returns a *BusyError
// when another holder has it, including one in this process.
func TryLock(stateDir, siteKey string, log *slog.Logger) (*Lock, error) {
	if log == nil {
		log = slog.Default()
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating state dir for site '%s' lock: %w", siteKey, err)
	}
	path := filepath.Join(stateDir, utils.SanitizeFilename(siteKey)+".lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening crawl lock for site '%s': %w", siteKey, err)
	}
	switch err := tryLockFile(f); {
	case err == nil:
	case errors.Is(err, errLocked):
		pid := readPID(f)
		_ = f.Close()
		return nil, &BusyError{Site: siteKey, PID: pid}
	case errors.Is(err, errUnsupported):
		_ = f.Close()
		log.Warn(fmt.Sprintf("file locking is not supported on %s; concurrent crawls of site '%s' are not prevented", stateDir, siteKey))
		return &Lock{site: siteKey}, nil
	default:
		_ = f.Close()
		return nil, fmt.Errorf("locking crawl lock for site '%s': %w", siteKey, err)
	}
	if err := writePID(f); err != nil {
		_ = platformUnlock(f)
		_ = f.Close()
		return nil, fmt.Errorf("writing crawl lock for site '%s': %w", siteKey, err)
	}
	return &Lock{site: siteKey, f: f}, nil
}

func (l *Lock) Site() string { return l.site }

// Unlock releases the lock. The lock file stays on disk: removing it would
// race a process that has it open and is about to lock it.
func (l *Lock) Unlock() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := platformUnlock(l.f)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	l.f = nil
	return err
}

func writePID(f *os.File) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	_, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return err
}

func readPID(f *os.File) int {
	buf := make([]byte, 32)
	n, _ := f.ReadAt(buf, 0)
	pid, err := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
