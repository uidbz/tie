//go:build !linux

package client

import (
	"errors"
	"os"
)

// reflink always declines off Linux: no portable clone ioctl in x/sys.
func reflink(_, _ *os.File) error {
	return errors.New("no reflink on this platform")
}
