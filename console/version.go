package console

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"

	"metatrash.com/metatrash"
	"metatrash.com/metatrash/internal/service"
)

// Component is a part of this build that `version` names beside Metatrash
// Core, such as an application built on Core.
type Component struct {
	Name    string
	Version string
}

var (
	extraComponents []Component
	frontendFS      fs.FS
)

// AddComponents names more parts of this build in `version`, after Core
// (0.95.13). An application built on Core calls it before Run.
func AddComponents(components ...Component) {
	extraComponents = append(extraComponents, components...)
}

// SetFrontend makes `version` read frontends.json from this site build
// instead of Core's own: the same files the application passes as
// server.Config.Frontend.
func SetFrontend(files fs.FS) { frontendFS = files }

// frontends is frontends.json: the front-end sources a build carries and,
// when the build wrote them, the libraries bundled into it.
type frontends struct {
	Slate     *frontendSource   `json:"slate"`
	Viewer    *frontendSource   `json:"viewer"`
	Libraries []frontendLibrary `json:"libraries"`
}

type frontendSource struct {
	Version     string `json:"version"`
	Commit      string `json:"commit"`
	Uncommitted bool   `json:"uncommitted"`
}

type frontendLibrary struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func readFrontends() (frontends, bool) {
	data := service.FrontendsJSON()
	if frontendFS != nil {
		var err error
		if data, err = fs.ReadFile(frontendFS, "frontends.json"); err != nil {
			return frontends{}, false
		}
	}
	var f frontends
	if json.Unmarshal(data, &f) != nil {
		return frontends{}, false
	}
	return f, true
}

func (s *frontendSource) String() string {
	text := s.Version
	if s.Commit != "" {
		text += " (" + shortCommit(s.Commit)
		if s.Uncommitted {
			text += ", with uncommitted changes"
		}
		text += ")"
	}
	return text
}

func shortCommit(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

// runVersion prints the version of every part of this build and the
// front-end libraries bundled into the site. -all adds the third-party Go
// libraries compiled in; -core prints only Core's version, as `version` did
// before 0.95.13 (release scripts compare it).
func runVersion(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("version", flag.ContinueOnError)
	all := flags.Bool("all", false, "also list the Go libraries compiled in")
	core := flags.Bool("core", false, "print only Metatrash Core's version")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("version takes no arguments")
	}
	if *core {
		fmt.Fprintln(out, metatrash.Version)
		return nil
	}
	rows := [][2]string{{"Metatrash Core", metatrash.Version}}
	for _, c := range extraComponents {
		rows = append(rows, [2]string{c.Name, c.Version})
	}
	front, haveFront := readFrontends()
	if haveFront && front.Viewer != nil {
		rows = append(rows, [2]string{"Slate Viewer (site)", front.Viewer.String()})
	}
	if haveFront && front.Slate != nil {
		rows = append(rows, [2]string{"Slate (editor)", front.Slate.String()})
	}
	info, haveInfo := debug.ReadBuildInfo()
	goVersion := runtime.Version()
	if haveInfo && info.GoVersion != "" {
		goVersion = info.GoVersion
	}
	rows = append(rows, [2]string{"Go", strings.TrimPrefix(goVersion, "go") + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")"})
	width := 0
	for _, row := range rows {
		width = max(width, len(row[0]))
	}
	for _, row := range rows {
		fmt.Fprintf(out, "%-*s  %s\n", width, row[0], row[1])
	}

	if haveInfo && *all {
		var libraries [][2]string
		for _, dep := range info.Deps {
			// Core itself is a module of an application built on it: it is listed above.
			if strings.HasPrefix(dep.Path, "metatrash.com/") {
				continue
			}
			if dep.Replace != nil && dep.Replace.Version != "" {
				dep = dep.Replace
			}
			libraries = append(libraries, [2]string{dep.Path, dep.Version})
		}
		printLibraries(out, "Go libraries (compiled in)", libraries)
	}
	if haveFront && len(front.Libraries) > 0 {
		libraries := make([][2]string, 0, len(front.Libraries))
		for _, l := range front.Libraries {
			libraries = append(libraries, [2]string{l.Name, l.Version})
		}
		printLibraries(out, "Front-end libraries (bundled into the site)", libraries)
	}
	return nil
}

func printLibraries(out io.Writer, title string, libraries [][2]string) {
	if len(libraries) == 0 {
		return
	}
	sort.Slice(libraries, func(i, j int) bool { return libraries[i][0] < libraries[j][0] })
	width := 0
	for _, l := range libraries {
		width = max(width, len(l[0]))
	}
	fmt.Fprintf(out, "\n%s:\n", title)
	for _, l := range libraries {
		fmt.Fprintf(out, "  %-*s  %s\n", width, l[0], l[1])
	}
}
