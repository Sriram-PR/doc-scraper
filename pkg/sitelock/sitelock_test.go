package sitelock

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTryLock_SecondAttemptInSameProcessIsBusy(t *testing.T) {
	dir := t.TempDir()
	first, err := TryLock(dir, "docs", nil)
	require.NoError(t, err)
	assert.Equal(t, "docs", first.Site())

	_, err = TryLock(dir, "docs", nil)
	require.ErrorIs(t, err, ErrBusy)
	var busy *BusyError
	require.ErrorAs(t, err, &busy)
	assert.Equal(t, os.Getpid(), busy.PID)
	assert.Equal(t, fmt.Sprintf("site 'docs' is already being crawled (pid %d)", os.Getpid()), err.Error())

	require.NoError(t, first.Unlock())
	again, err := TryLock(dir, "docs", nil)
	require.NoError(t, err)
	require.NoError(t, again.Unlock())
}

func TestTryLock_KeysThatSanitizeAlikeShareTheLock(t *testing.T) {
	dir := t.TempDir()
	l, err := TryLock(dir, "a/b", nil)
	require.NoError(t, err)
	defer func() { _ = l.Unlock() }()

	_, err = TryLock(dir, "a_b", nil)
	assert.ErrorIs(t, err, ErrBusy)
}

func TestUnlock_IsIdempotentAndKeepsTheFile(t *testing.T) {
	dir := t.TempDir()
	l, err := TryLock(dir, "docs", nil)
	require.NoError(t, err)
	require.NoError(t, l.Unlock())
	require.NoError(t, l.Unlock())
	assert.FileExists(t, filepath.Join(dir, "docs.lock"))
}

func TestTryLock_UnreadablePIDGivesBusyWithoutPID(t *testing.T) {
	dir := t.TempDir()
	l, err := TryLock(dir, "docs", nil)
	require.NoError(t, err)
	defer func() { _ = l.Unlock() }()

	f, err := os.OpenFile(filepath.Join(dir, "docs.lock"), os.O_RDWR, 0)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(0))
	_, err = f.WriteAt([]byte("garbage"), 0)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	_, err = TryLock(dir, "docs", nil)
	var busy *BusyError
	require.ErrorAs(t, err, &busy)
	assert.Zero(t, busy.PID)
	assert.Equal(t, "site 'docs' is already being crawled", err.Error())
}

func TestTryLock_UnsupportedFilesystemWarnsAndProceeds(t *testing.T) {
	orig := tryLockFile
	tryLockFile = func(*os.File) error { return errUnsupported }
	t.Cleanup(func() { tryLockFile = orig })

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	dir := t.TempDir()

	l, err := TryLock(dir, "docs", log)
	require.NoError(t, err)
	var rec struct{ Level, Msg string }
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rec))
	assert.Equal(t, "WARN", rec.Level)
	assert.Equal(t, fmt.Sprintf("file locking is not supported on %s; concurrent crawls of site 'docs' are not prevented", dir), rec.Msg)
	require.NoError(t, l.Unlock())
}

func TestTryLock_OtherErrorsAreReturned(t *testing.T) {
	orig := tryLockFile
	boom := errors.New("boom")
	tryLockFile = func(*os.File) error { return boom }
	t.Cleanup(func() { tryLockFile = orig })

	_, err := TryLock(t.TempDir(), "docs", nil)
	require.ErrorIs(t, err, boom)
	assert.NotErrorIs(t, err, ErrBusy)
}

// TestHelperHoldLock is the child process for
// TestTryLock_HeldByOtherProcessReleasedOnKill.
func TestHelperHoldLock(t *testing.T) {
	dir := os.Getenv("SITELOCK_HELPER_DIR")
	if dir == "" {
		t.Skip("helper process only")
	}
	l, err := TryLock(dir, "docs", nil)
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(2)
	}
	fmt.Println("locked")
	time.Sleep(time.Minute)
	_ = l.Unlock()
	os.Exit(0)
}

func TestTryLock_HeldByOtherProcessReleasedOnKill(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldLock$")
	cmd.Env = append(os.Environ(), "SITELOCK_HELPER_DIR="+dir)
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	line, err := bufio.NewReader(out).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "locked\n", line)

	_, err = TryLock(dir, "docs", nil)
	var busy *BusyError
	require.ErrorAs(t, err, &busy)
	assert.Equal(t, cmd.Process.Pid, busy.PID)

	require.NoError(t, cmd.Process.Kill())
	_ = cmd.Wait()
	require.Eventually(t, func() bool {
		l, err := TryLock(dir, "docs", nil)
		if err != nil {
			return false
		}
		_ = l.Unlock()
		return true
	}, 10*time.Second, 50*time.Millisecond, "the OS releases a killed holder's lock")
}
