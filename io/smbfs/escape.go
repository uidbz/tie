package smbfs

import (
	"strings"
)

// SMB forbids a set of characters in file and directory names, and Windows
// clients additionally refuse trailing dots/spaces and device names. tie names
// are arbitrary strings (tag queries even contain spaces and colons), so
// every name this share lists is escaped into an SMB-legal form and unescaped
// again when a client asks for it back. The escape is reversible and
// collision-free:
//
//   - each illegal or control byte -> "~XX" (lowercase hex of the byte);
//     "~" itself -> "~7e", so the marker can never be ambiguous;
//   - a trailing "." or " " -> "~2e" / "~20" (SMB refuses trailing dot/space);
//   - a name whose base (up to the first ".") is a Win32 reserved device name
//     has its first byte escaped the same way ("CON.txt" -> "~43ON.txt"), so
//     plain "~XX" decoding reverses it with no special case.
//
// The escape never produces ":" - ":" itself becomes "~3a" - which matters
// because the smb library's smbPathToFS rejects any path containing ":" (ADS
// stream syntax): escaped names can therefore always be resolved.
const escapeMarker = '~'

// illegalBytes are the characters SMB forbids in a name (MS-SMB2 2.1.4 and
// the Windows filename rules); bytes below 0x20 and 0x7f are refused too.
const illegalBytes = `\/*:"<>|?`

// reservedNames are the Win32 device names, case-insensitive, refused as the
// base name (everything before the first dot) of a file or directory.
var reservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

const hexDigits = "0123456789abcdef"

// escapeByte writes c as "~XX".
func escapeByte(b *strings.Builder, c byte) {
	b.WriteByte(escapeMarker)
	b.WriteByte(hexDigits[c>>4])
	b.WriteByte(hexDigits[c&0xf])
}

// escapeStr is escapeByte as a string.
func escapeStr(c byte) string {
	return string(escapeMarker) + string(hexDigits[c>>4]) + string(hexDigits[c&0xf])
}

// mustEscape reports whether byte c needs the ~XX escape in a display name.
func mustEscape(c byte) bool {
	return c < 0x20 || c == 0x7f || c == escapeMarker || strings.IndexByte(illegalBytes, c) >= 0
}

// escapeName maps one tie name onto the SMB-legal display name a client sees.
func escapeName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if mustEscape(c) {
			escapeByte(&b, c)
			continue
		}
		b.WriteByte(c)
	}
	out := b.String()
	if out == "" {
		return ""
	}
	// SMB refuses a trailing dot or space. The escaped form ends in the same
	// byte as the original (only bytes below 0x80 are rewritten, and never at
	// the tail unless they are the tail), so checking the last byte of out is
	// enough. A name of only dots ("..." - "." and ".." are filtered by the
	// server itself) is illegal too; escaping its final dot breaks the run.
	if last := out[len(out)-1]; last == '.' || last == ' ' {
		out = escapeName(name[:len(name)-1]) + escapeStr(last)
		// The recursion above may have reserved-escaped the base already; the
		// tail escape cannot create a reserved base, so return directly.
		return out
	}
	// Win32 refuses reserved device names: escape the first byte of the
	// original name (always an ASCII letter there, so it is unescaped in out).
	if isReservedBase(name) {
		out = escapeStr(name[0]) + out[1:]
	}
	return out
}

// isReservedBase reports whether the base name (up to the first dot,
// case-insensitively) is a Win32 device name.
func isReservedBase(name string) bool {
	base := name
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	return reservedNames[strings.ToUpper(base)]
}

// unescapeName maps a client-supplied display name back to the tie name.
// "~XX" decodes to byte 0xXX; a "~" not followed by two hex digits is kept
// literally (a client could invent one; keeping it beats failing the whole
// path, and it simply will not match any stored name).
func unescapeName(name string) string {
	if strings.IndexByte(name, escapeMarker) < 0 {
		return name
	}
	var b strings.Builder
	b.Grow(len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == escapeMarker && i+2 < len(name) {
			hi, ok1 := hexVal(name[i+1])
			lo, ok2 := hexVal(name[i+2])
			if ok1 && ok2 {
				b.WriteByte(hi<<4 | lo)
				i += 2
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
