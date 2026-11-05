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
