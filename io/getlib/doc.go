// Package getlib downloads files and directory trees from a tie-filehost.
//
// The filehost is treated as untrusted. Every blob is re-hashed as it is read
// and rejected with [ErrChecksum] if it does not match the content address it
// was requested under, so tampered or truncated transfers never reach the
// caller as genuine content. File bodies stream through the hasher instead of
// being buffered whole, and [Cache.ReadFile] persists a downloaded blob only
// after verifying it, writing atomically so a failed transfer leaves no corrupt
// cache entry.
//
// Directory manifests are parsed by the metadata package, which enforces that
// each entry names a single safe path component and a well-formed 64-hex-char
// hash; getlib additionally caps directory recursion depth. Together these stop
// a crafted manifest from escaping the checkout root, redirecting a fetch, or
// driving unbounded recursion.
//
// DownloadFile checks a whole tree out to a destination directory. ExecForEach
// is the general driver: it invokes a TieFunc for every verified file in the
// tree. TotalSize reports the byte total a download would transfer, for sizing
// a progress bar before the transfer begins.
package getlib
