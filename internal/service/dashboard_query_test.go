package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"metatrash.com/metatrash"
)

func TestDashboardQuery(t *testing.T) {
	s := openPublicTestService(t)
	ctx := context.Background()
	state := func() string {
		v, err := s.Dispatch(ctx, "public", "list", Input{})
		if err != nil {
			t.Fatal(err)
		}
		return v.(ListResult).State
	}
	write := func(path, text string) {
		t.Helper()
		if _, err := s.Dispatch(ctx, "public", "write", Input{Path: path, Text: &text, IfInState: state()}); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write("service/jobs/fix-pump.md", "---\ntitle: Fix pump\nstate: open\ntype: Repair\nassignee: pat\ndue: 2026-11-10\nhours: 3\n---\nNotes.\n")
	write("service/jobs/service-boiler.md", "---\ntitle: Service boiler\nstate: open\ntype: Service\nassignee: sam\ndue: 2026-11-30\nhours: 1.5\n---\n")
	write("service/jobs/paint.md", "---\ntitle: Paint\nstate: done\ntype: Repair\nassignee: pat\ntags: [inside, quick]\nhours: 2\n---\n")
	// A filed form copy: its answers are fields too; status is the copy's.
	write("service/jobs/public-pat-job-2026-11-17T0900Z.md", "---\nform: job\ntitle: \"Job\"\nsubmittedBy: \"pat\"\nsubmittedAt: 2026-11-17T09:00:00Z\nstatus: complete\nanswers:\n  title: \"Fix gate\"\n  state: \"open\"\n  type: \"Repair\"\n  assignee: \"pat\"\n  hours: \"4\"\n  parts:\n---\nbody\n")
	write("service/sales.csv", "month,region,amount\n2026-09,North,100\n2026-09,South,50\n2026-10,North,\"1,200\"\n\n")
	query := func(me string, in QueryInput, code string) QueryResult {
		t.Helper()
		r := s.repos["public"]
		v, err := s.query(ctx, r, "public", me, in)
		if code != "" {
			if e, ok := err.(*Error); !ok || e.Code != code {
				t.Fatalf("%+v: got %v, want %s", in, err, code)
			}
			return v
		}
		if err != nil {
			t.Fatalf("%+v: %v", in, err)
		}
		return v
	}

	// Count of open jobs; the copy counts by its answers.
	got := query("", QueryInput{Base: "service/dashboard.md", Source: "jobs/", Where: "state!=done"}, "")
	if got.Count != 3 || got.Value != 3 || got.Kind != "folder" || got.Source != "service/jobs/" {
		t.Fatalf("open: %+v", got)
	}
	// Grouped, largest first.
	got = query("", QueryInput{Source: "/service/jobs/", Where: "state=open", Group: "type"}, "")
	if len(got.Groups) != 2 || got.Groups[0] != (QueryGroup{Group: "Repair", Count: 2, Value: 2}) || got.Groups[1].Group != "Service" {
		t.Fatalf("groups: %+v", got.Groups)
	}
	// @me, sorting and columns.
	got = query("pat", QueryInput{Base: "service/d.md", Source: "jobs/", Where: "assignee=@me; state!=done", Sort: "-hours", Columns: "title, hours"}, "")
	if got.Count != 2 || got.Rows[0].Values["title"] != "Fix gate" || got.Rows[1].Values["title"] != "Fix pump" || len(got.Rows[0].Values) != 2 || got.Rows[1].File != "service/jobs/fix-pump.md" {
		t.Fatalf("mine: %+v", got.Rows)
	}
	// Dates with today, sums and averages.
	got = query("", QueryInput{Source: "/service/jobs/", Where: "due<today", Today: "2026-11-17"}, "")
	if got.Count != 1 {
		t.Fatalf("overdue: %+v", got)
	}
	got = query("", QueryInput{Source: "/service/jobs/", Measure: "sum hours", Group: "assignee", Sort: "group"}, "")
	if got.Value != 10.5 || got.Groups[0].Group != "pat" || got.Groups[0].Value != 9 {
		t.Fatalf("sum: %+v", got)
	}
	// Lists give several values; a missing field is not answered.
	got = query("", QueryInput{Source: "/service/jobs/", Where: "tags=quick"}, "")
	if got.Count != 1 {
		t.Fatalf("list: %+v", got)
	}
	got = query("", QueryInput{Source: "/service/jobs/", Where: "!due"}, "")
	if got.Count != 2 {
		t.Fatalf("missing: %+v", got)
	}
	// CSV with a number written with a comma.
	got = query("", QueryInput{Source: "/service/sales.csv", Group: "month", Measure: "sum amount", Sort: "group"}, "")
	if got.Kind != "csv" || got.Count != 3 || got.Groups[1] != (QueryGroup{Group: "2026-10", Count: 1, Value: 1200}) || got.Rows != nil {
		t.Fatalf("csv: %+v", got)
	}
	got = query("", QueryInput{Source: "/service/sales.csv", Where: "region=north", Limit: 1}, "")
	if got.Count != 2 || len(got.Rows) != 1 || !got.Truncated || got.Rows[0].Line != 2 {
		t.Fatalf("csv rows: %+v", got)
	}
	// Mistakes.
	query("", QueryInput{Source: "/service/nothing/"}, "not_found")
	query("", QueryInput{Source: "/service/sales.txt"}, "invalid_request")
	query("", QueryInput{Source: "/service/jobs/", Where: "state ~ x"}, "invalid_request")
	query("", QueryInput{Source: "/service/jobs/", Measure: "median hours"}, "invalid_request")
	query("", QueryInput{Source: "/service/jobs/", Limit: 501}, "invalid_request")

	// A changed file shows on the next query (the cache follows blobs).
	write("service/jobs/paint.md", "---\ntitle: Paint\nstate: open\ntype: Repair\n---\n")
	if got = query("", QueryInput{Source: "/service/jobs/", Where: "state=open"}, ""); got.Count != 4 {
		t.Fatalf("after change: %+v", got)
	}

	// A pattern lists matching files as rows (Core 0.102.0): any file type,
	// front matter for Markdown, never AGENTS.md or a dot folder.
	write("service/forms/leave.md", "---\ntitle: Leave request\n---\nform\n")
	write("service/forms/expenses.md", "---\ntitle: Expenses\n---\n")
	write("service/forms/notes.txt", "plain\n")
	write("service/forms/AGENTS.md", "# Forms\n")
	write("service/forms/old/2025.md", "old\n")
	got = query("", QueryInput{Base: "service/dashboard.md", Source: "forms/*.md"}, "")
	if got.Kind != "files" || got.Source != "service/forms/*.md" || got.Count != 2 || got.Rows[0].File != "service/forms/expenses.md" || got.Rows[0].Values["title"] != "Expenses" || got.Rows[1].Values["name"] != "leave" {
		t.Fatalf("pattern: %+v", got)
	}
	got = query("", QueryInput{Source: "/service/forms/*"}, "")
	if got.Count != 3 || got.Rows[1].Values["folder"] != "service/forms" {
		t.Fatalf("star: %+v", got.Rows)
	}
	got = query("", QueryInput{Source: "/service/**/*.MD", Where: "folder=service/forms/old"}, "")
	if got.Count != 1 || got.Rows[0].File != "service/forms/old/2025.md" {
		t.Fatalf("any folders: %+v", got.Rows)
	}
	if got = query("", QueryInput{Source: "/nothing/*.md"}, ""); got.Count != 0 {
		t.Fatalf("no match: %+v", got)
	}
	for _, bad := range []string{"forms/*/", "a**b/*.md", "forms/*.md\x00"} {
		query("", QueryInput{Base: "service/dashboard.md", Source: bad}, "invalid_request")
	}

	// REST: POST .../query.
	h, err := s.Handler(metatrash.ToolSchema, nil)
	if err != nil {
		t.Fatal(err)
	}
	post := func(body string) (int, QueryResult) {
		r := httptest.NewRequest("POST", "/api/v1/spaces/public/query", bytes.NewReader([]byte(body)))
		r.RemoteAddr = "127.0.0.1:1"
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out QueryResult
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, rest := post(`{"source": "/service/jobs/", "group": "type"}`); code != 200 || len(rest.Groups) != 2 {
		t.Fatalf("REST: %d %+v", code, rest)
	}
	if code, _ := post(`{"source": "x/", "nope": 1}`); code != 400 {
		t.Fatalf("unknown key: %d", code)
	}
}

func TestQueryPatternMatch(t *testing.T) {
	for _, c := range []struct {
		pattern, path string
		want          bool
	}{
		{"a/*.md", "a/x.md", true},
		{"a/*.md", "a/b/x.md", false},
		{"a/**/*.md", "a/x.md", true},
		{"a/**/*.md", "a/b/c/x.md", true},
		{"**/*.dash.md", "x.dash.md", true},
		{"a/?.md", "a/xy.md", false},
		{"a/*-[1].md", "a/x-[1].md", true},
		{"a/*.MD", "A/b.md", true},
		{"a/*", "a", false},
	} {
		if got := matchQueryPattern(c.pattern, c.path); got != c.want {
			t.Errorf("%s %s: %v", c.pattern, c.path, got)
		}
	}
}

func TestQueryWhereParse(t *testing.T) {
	for _, bad := range []string{"=x", "a ~ b", "a>", "1a=b"} {
		if _, err := parseQueryWhere(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
	cs, err := parseQueryWhere(" state=open|new ; !done; hours>=2 ")
	if err != nil || len(cs) != 3 || cs[0].op != "=" || len(cs[0].values) != 2 || cs[1].op != "!" || cs[2].op != ">=" {
		t.Fatalf("%+v %v", cs, err)
	}
}

func TestFolderHomeSetting(t *testing.T) {
	for text, ok := range map[string]bool{
		`{"home": "dashboard.md"}`: true,
		`{"home": "sub/x.md"}`:     false,
		`{"home": "x.txt"}`:        false,
		`{"home": "AGENTS.md"}`:    false,
	} {
		_, err := parseFolderConfig(text)
		if (err == nil) != ok {
			t.Errorf("%s: %v", text, err)
		}
	}
}
