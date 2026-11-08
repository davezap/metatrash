// Package filenames defines storage paths shared by the service and sync client.
package filenames

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Segment accepts Unicode and punctuation without changing spelling or case.
// Separators, control characters and Git's reserved directory are not filenames.
func Segment(name string) bool {
	if name == "" || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if r == '/' || r == '\\' || unicode.IsControl(r) {
			return false
		}
	}
	// Include Windows aliases so metadata cannot be reached through another name.
	alias := strings.TrimRight(strings.ToLower(name), ". ")
	return alias != "" && alias != ".git"
}

// Path is relative to a space. Limits remain 240 UTF-8 bytes and eight segments.
// The service index and the local client's state directory are reserved at root.
func Path(path string) bool {
	if len(path) > 240 {
		return false
	}
	segments := strings.Split(path, "/")
	if len(segments) > 8 {
		return false
	}
	for i, name := range segments {
		if !Segment(name) {
			return false
		}
		if i == 0 {
			alias := strings.TrimRight(strings.ToLower(name), ". ")
			if alias == ".metatrash" || alias == ".mtsync" {
				return false
			}
		}
	}
	return true
}

// NewName says why path can't be given to a new file or folder ("" when it
// can), 0.95.11. Characters that change how a name reads without being seen
// are refused: direction overrides and marks (invoice<RLO>fdp.exe shows as
// invoiceexe.pdf), zero-width spaces and joiners other than ZWJ/ZWNJ, the
// byte-order mark, soft hyphens, invisible fillers and line separators. ZWJ
// and ZWNJ stay because emoji sequences and several scripts need them.
// Existing files keep their names: Path still accepts them, so they can be
// read, renamed and deleted.
func NewName(path string) string {
	for _, r := range path {
		if hiddenRune(r) {
			return "names can't hold invisible or direction-changing characters (" + runeName(r) + ")"
		}
	}
	return ""
}

func hiddenRune(r rune) bool {
	switch {
	case r == 0x00AD, r == 0x061C, r == 0x115F, r == 0x1160, r == 0x180E,
		r == 0x200B, r == 0x200E, r == 0x200F,
		r >= 0x2028 && r <= 0x202E,
		r >= 0x2060 && r <= 0x2064,
		r >= 0x2066 && r <= 0x2069,
		r == 0x3164, r == 0xFEFF, r == 0xFFA0:
		return true
	}
	return false
}

func runeName(r rune) string {
	const hex = "0123456789ABCDEF"
	b := []byte("U+0000")
	for i := 5; i >= 2; i-- {
		b[i] = hex[r&0xF]
		r >>= 4
	}
	return string(b)
}
