//go:build !unix

package tiedb

import "os"

// lockCollectionFile is a no-op on platforms without flock. The shipped
// binaries target Unix (FUSE/SMB), but the engine still builds elsewhere.
func lockCollectionFile(string) (*os.File, error) { return nil, nil }
