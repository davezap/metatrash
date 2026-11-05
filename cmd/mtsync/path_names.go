package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"metatrash.com/metatrash/internal/filenames"
)

// Compare the existing state directory through a different case in its parent.
// This probes the sync root itself without creating a visible temporary file.
func insensitiveFolder(root string) (bool, error) {
	original, err := os.Stat(filepath.Join(root, stateDir))
	if err != nil {
		return false, err
	}
	other, err := os.Stat(filepath.Join(root, strings.ToUpper(stateDir)))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return os.SameFile(original, other), nil
}

func foldedPath(path string) string {
	return strings.Map(func(r rune) rune {
		least := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < least {
				least = next
			}
		}
		return least
	}, path)
}

func windowsNameProblem(path string) string {
	for _, name := range strings.Split(path, "/") {
		if strings.ContainsAny(name, `<>:"|?*`) || strings.HasSuffix(name, " ") || strings.HasSuffix(name, ".") {
			return "Windows cannot create this name"
		}
		stem, _, _ := strings.Cut(name, ".")
		stem = strings.TrimRight(strings.ToUpper(stem), " ")
		// DOS 8.3 aliases can resolve to another file, including client state.
		// Keep alias-shaped names online rather than risk writing through them.
		if tilde := strings.LastIndex(stem, "~"); tilde >= 0 && tilde <= 6 && len(stem) <= 8 && tilde < len(stem)-1 {
			digits := true
			for _, r := range stem[tilde+1:] {
				if r < '0' || r > '9' {
					digits = false
				}
			}
			if digits {
				return "Windows short filename aliases cannot sync safely"
			}
		}
		if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || stem == "CONIN$" || stem == "CONOUT$" ||
			((strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && len([]rune(stem)) == 4 && strings.ContainsRune("123456789¹²³", []rune(stem)[3])) {
			return "Windows reserves this device name"
		}
	}
	return ""
}

// Check every prefix too: Foo/a and foo/b alias the same directory even though
// the complete filenames differ. All sides participate, including deletions.
// No skipped path is renamed, agreed, uploaded, overwritten or deleted.
func pathProblems(paths []string, windows, insensitive bool) map[string]string {
	problems := map[string]string{}
	prefixes := map[string]string{}
	collisions := map[string]bool{}
	for _, path := range paths {
		if !filenames.Path(path) {
			problems[path] = "invalid or reserved storage path"
			continue
		}
		if windows {
			if reason := windowsNameProblem(path); reason != "" {
				problems[path] = reason
			}
		}
		if !insensitive {
			continue
		}
		parts := strings.Split(path, "/")
		for i := range parts {
			prefix := strings.Join(parts[:i+1], "/")
			key := foldedPath(prefix)
			if previous, ok := prefixes[key]; ok && previous != prefix {
				collisions[key] = true
			}
			prefixes[key] = prefix
		}
	}
	if insensitive {
		for _, path := range paths {
			parts := strings.Split(path, "/")
			for i := range parts {
				if collisions[foldedPath(strings.Join(parts[:i+1], "/"))] {
					problems[path] = "names differ only by case on a case-insensitive disk"
					break
				}
			}
		}
	}
	return problems
}

func describePathProblems(paths []string, problems map[string]string) error {
	var examples []string
	for _, path := range paths {
		if reason, ok := problems[path]; ok && len(examples) < 3 {
			examples = append(examples, fmt.Sprintf("%q: %s", path, reason))
		}
	}
	return fmt.Errorf("%d paths cannot sync on this computer; their local and server copies were left unchanged: %s", len(problems), strings.Join(examples, "; "))
}
