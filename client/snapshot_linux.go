//go:build linux

package client

import (
	"os"

	"golang.org/x/sys/unix"
)

// reflink asks the filesystem (btrfs, XFS, ...) to share extents between dst
// and src; a non-nil error means "copy it instead".
func reflink(dst, src *os.File) error {
	return unix.IoctlSetInt(int(dst.Fd()), unix.FICLONE, int(src.Fd()))
}
