package client

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// snapshotCollectionDir copies the regular .tie files of dir into a private
// (0700) temporary directory inside dir and returns it. A read-only direct
// open is documented as a load-time snapshot (#147): serving reads from this
// copy is what makes that promise true — the live file keeps moving under a
// reader otherwise (resolveEntry re-reads records on demand). The temp dir
// lives inside dir so the copy stays on one filesystem and reflink can work;
// remove the returned directory with os.RemoveAll on Close.
func snapshotCollectionDir(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	snap, err := os.MkdirTemp(dir, ".tie-snapshot-")
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".tie") {
			continue
		}
		src := filepath.Join(dir, name)
		fi, serr := os.Stat(src)
		if serr != nil || !fi.Mode().IsRegular() {
			continue // a dangling link, a FIFO: not something to snapshot
		}
		if cerr := reflinkOrCopy(src, filepath.Join(snap, name)); cerr != nil {
			_ = os.RemoveAll(snap)
			return "", cerr
		}
	}
	return snap, nil
}

// reflinkOrCopy clones src to dst cheaply where the filesystem supports it
// and copies byte-for-byte otherwise.
func reflinkOrCopy(src, dst string) error {
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()
	d, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if reflink(d, s) == nil {
		return d.Close()
	}
	if _, err := io.Copy(d, s); err != nil {
		_ = d.Close()
		return fmt.Errorf("copying %s: %w", src, err)
	}
	return d.Close()
}
