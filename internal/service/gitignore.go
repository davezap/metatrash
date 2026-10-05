package service

import (
	"context"
	"regexp"
	"sort"
	"strings"
)

// .gitignore support for GitHub folders: push sends the folder's files minus
// what its .gitignore files exclude, as git would. Supported: comments, blank
// lines, trailing-space trimming, "!" negation, trailing "/" (directories
// only), leading or inner "/" (anchored to the .gitignore's folder), "*", "?",
// "**" (leading, trailing and inner), [classes] with "!" or "^", backslash
// escapes, .gitignore files in subfolders, and git's rule that a file inside
// an excluded folder cannot be re-included. Not read: .git/info/exclude and
// global excludes, which do not exist for a mirrored folder.

type ignoreRule struct {
	base     string // folder of the .gitignore, relative to the repo, "" or ending in "/"
	re       *regexp.Regexp
	negate   bool
	dirOnly  bool
	anchored bool // matched against the path below base, not just the name
}

// parseGitignore reads one .gitignore whose folder is base ("" or "dir/").
// Lines that cannot be turned into a pattern are skipped, as git does.
func parseGitignore(base, text string) []ignoreRule {
	var rules []ignoreRule
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		line = trimGitignoreSpaces(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rule := ignoreRule{base: base}
		if strings.HasPrefix(line, "!") {
			rule.negate = true
			line = line[1:]
		} else if strings.HasPrefix(line, `\!`) || strings.HasPrefix(line, `\#`) {
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") && !strings.HasSuffix(line, `\/`) {
			rule.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		if line == "" {
			continue
		}
		if strings.Contains(line, "/") {
			rule.anchored = true
			line = strings.TrimPrefix(line, "/")
		}
		expr, ok := gitignoreRegexp(line)
		if !ok {
			continue
		}
		re, err := regexp.Compile("^" + expr + "$")
		if err != nil {
			continue
		}
		rule.re = re
		rules = append(rules, rule)
	}
	return rules
}

// trimGitignoreSpaces drops trailing spaces unless escaped with a backslash.
func trimGitignoreSpaces(line string) string {
	end := len(line)
	for end > 0 && line[end-1] == ' ' {
		if end >= 2 && line[end-2] == '\\' {
			break
		}
		end--
	}
	return line[:end]
}

// gitignoreRegexp converts one wildmatch pattern (no leading "/") to a regexp.
func gitignoreRegexp(p string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '*' && i+1 < len(p) && p[i+1] == '*' && (i == 0 || p[i-1] == '/') && (i+2 == len(p) || p[i+2] == '/'):
			// "**" as a whole segment.
			switch {
			case i+2 == len(p) && i == 0:
				b.WriteString(".*")
			case i+2 == len(p):
				b.WriteString(".*") // "dir/**": everything inside
			default:
				b.WriteString("(?:.*/)?") // "**/" leading or inner: zero or more folders
				i++                       // skip the slash after "**"
			}
			i++
		case c == '*':
			for i+1 < len(p) && p[i+1] == '*' {
				i++ // "**" not on its own behaves like "*"
			}
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '[':
			end := i + 1
			if end < len(p) && (p[end] == '!' || p[end] == '^') {
				end++
			}
			if end < len(p) && p[end] == ']' {
				end++
			}
			for end < len(p) && p[end] != ']' {
				if p[end] == '\\' {
					end++
				}
				end++
			}
			if end >= len(p) {
				return "", false // unterminated class never matches in git
			}
			class := p[i+1 : end]
			b.WriteString("[")
			if strings.HasPrefix(class, "!") || strings.HasPrefix(class, "^") {
				b.WriteString("^/")
				class = class[1:]
			}
			for j := 0; j < len(class); j++ {
				switch class[j] {
				case '\\':
					if j+1 < len(class) {
						j++
						b.WriteString(regexp.QuoteMeta(string(class[j])))
					}
				case '[', ']', '^':
					b.WriteString(`\` + string(class[j]))
				default:
					b.WriteByte(class[j])
				}
			}
			b.WriteString("]")
			i = end
		case c == '\\':
			if i+1 < len(p) {
				i++
				b.WriteString(regexp.QuoteMeta(string(p[i])))
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String(), true
}

// ignoreMatcher answers whether paths of one repo are ignored.
type ignoreMatcher struct {
	rules []ignoreRule // shallow .gitignore files first, file order within each
}

// newIgnoreMatcher builds a matcher from the repo's .gitignore files, keyed by
// their path relative to the repo ("" folder for ".gitignore", "a/" for
// "a/.gitignore").
func newIgnoreMatcher(gitignores map[string]string) *ignoreMatcher {
	paths := make([]string, 0, len(gitignores))
	for p := range gitignores {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool {
		di, dj := strings.Count(paths[i], "/"), strings.Count(paths[j], "/")
		if di != dj {
			return di < dj
		}
		return paths[i] < paths[j]
	})
	m := &ignoreMatcher{}
	for _, p := range paths {
		m.rules = append(m.rules, parseGitignore(strings.TrimSuffix(p, ".gitignore"), gitignores[p])...)
	}
	return m
}

// match applies the rules to one path (a file, or a folder when dir is set).
// The last matching rule wins.
func (m *ignoreMatcher) match(path string, dir bool) bool {
	ignored := false
	name := path[strings.LastIndex(path, "/")+1:]
	for _, rule := range m.rules {
		if rule.dirOnly && !dir {
			continue
		}
		if !strings.HasPrefix(path, rule.base) || path == strings.TrimSuffix(rule.base, "/") {
			continue
		}
		subject := name
		if rule.anchored {
			subject = strings.TrimPrefix(path, rule.base)
		}
		if rule.re.MatchString(subject) {
			ignored = !rule.negate
		}
	}
	return ignored
}

// ignored reports whether a file (relative to the repo) is excluded, either
// itself or because a folder above it is: git never looks inside an excluded
// folder, so a negation cannot bring back a file within it.
func (m *ignoreMatcher) ignored(path string) bool {
	for i := 0; i < len(path); i++ {
		if path[i] == '/' && m.match(path[:i], true) {
			return true
		}
	}
	return m.match(path, false)
}

// repoFiles splits the files of a GitHub folder into what push would send
// and what it leaves out, both as paths relative to the folder and sorted.
// Left out: .gitignore exclusions and every .metatrash.json, which describes
// Metatrash folders and is never part of the repo.
func (r *repository) repoFiles(ctx context.Context, files map[string]record, folder string) (send, skip []string, err error) {
	gitignores := map[string]string{}
	for p, f := range files {
		if rel, ok := strings.CutPrefix(p, folder); ok && (rel == ".gitignore" || strings.HasSuffix(rel, "/.gitignore")) {
			b, err := r.blob(ctx, f.Blob)
			if err != nil {
				return nil, nil, err
			}
			gitignores[rel] = string(b)
		}
	}
	m := newIgnoreMatcher(gitignores)
	for p := range files {
		rel, ok := strings.CutPrefix(p, folder)
		if !ok {
			continue
		}
		if isFolderConfig(rel) || m.ignored(rel) {
			skip = append(skip, rel)
		} else {
			send = append(send, rel)
		}
	}
	sort.Strings(send)
	sort.Strings(skip)
	return send, skip, nil
}
