//go:build unix

package tiedb

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

var lockUnsupportedOnce sync.Once

// lockCollectionFile takes an exclusive, non-blocking advisory lock on path.
// The returned handle must be closed to release it. A nil handle with a nil
// error means the filesystem does not support flock; the caller proceeds
// unlocked rather than refusing to open a database on such a mount.
//
// The lock is advisory: it only excludes other processes that also take it (a
// tiedb-backed server and a direct `tie -f` open). It cannot stop an unrelated
// program from writing the file.
func lockCollectionFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o666)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, fmt.Errorf("database %s is locked by another process (is tie-triplestore or another tie command running?)", path)
		}
		if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EINVAL) {
			lockUnsupportedOnce.Do(func() {
				slog.Warn("filesystem does not support advisory locks; the database may be opened by more than one process", "path", path)
			})
			return nil, nil
		}
		return nil, err
	}
	return f, nil
}
