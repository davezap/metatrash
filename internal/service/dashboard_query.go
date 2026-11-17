package service

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Dashboard queries (1.0 phase 1, Core 0.99.0). A dashboard card names a
// source (a folder of Markdown files, one row each, or a CSV file) and a
// small structured query over it: filters, grouping and a measure. The
// server answers it from the files at head, so the files stay the truth and
// the result follows every saved change. Rows of one source are parsed once
// per set of blobs and kept in a small cache.

const (
	maxQuerySourceRows = 5000
	maxQueryLimit      = 500
	maxQueryText       = 2000
	maxQueryColumns    = 30
	queryCacheEntries  = 64
)

// QueryInput is a card's query, as the card fence writes it.
type QueryInput struct {
	// Base is the dashboard file the card is in; a source without a leading
	// slash is read from its folder.
	Base    string `json:"base,omitempty"`
	Source  string `json:"source"`
	Where   string `json:"where,omitempty"`
	Group   string `json:"group,omitempty"`
	Measure string `json:"measure,omitempty"`
	Sort    string `json:"sort,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	// Columns lists the fields to return for each row (comma separated);
	// empty returns them all.
	Columns string `json:"columns,omitempty"`
	// Today is the viewer's date (YYYY-MM-DD) for `today` in where; the
	// server's date in the space's zone when absent.
	Today string `json:"today,omitempty"`
}

// QueryResult answers a card's query. Rows is set without group, Groups with
// it. Count is the number of matching rows either way.
type QueryResult struct {
	Space  string `json:"space"`
	State  string `json:"state"`
	Source string `json:"source"`
	Kind   string `json:"kind"`
	// Fields are the source's field names, in a stable order.
	Fields []string     `json:"fields"`
	Rows   []QueryRow   `json:"rows,omitempty"`
	Groups []QueryGroup `json:"groups,omitempty"`
	Count  int          `json:"count"`
	// Value is the measure over every matching row (the count, or the sum,
	// average, smallest or largest of a field).
	Value     float64 `json:"value"`
	Measure   string  `json:"measure"`
	Truncated bool    `json:"truncated,omitempty"`
}

// QueryRow is one matching row: its file (or CSV line) and its values.
type QueryRow struct {
	File   string            `json:"file,omitempty"`
	Line   int               `json:"line,omitempty"`
	Values map[string]string `json:"values"`
}

// QueryGroup is one group: its value of the group field, how many rows are
// in it, and the measure over them.
type QueryGroup struct {
	Group string  `json:"group"`
	Count int     `json:"count"`
	Value float64 `json:"value"`
}

// queryRow is a parsed row: each field's values (more than one for a list,
// such as a choice with multi).
type queryRow struct {
	file   string
	line   int
	values map[string][]string
}

type querySource struct {
	kind   string
	fields []string
	rows   []queryRow
}

type queryCache struct {
	mu      sync.Mutex
	entries map[string]*querySource
	order   []string
}

func (c *queryCache) get(key string) *querySource {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries[key]
}

func (c *queryCache) put(key string, src *querySource) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]*querySource{}
	}
	if _, ok := c.entries[key]; !ok {
		c.order = append(c.order, key)
	}
	c.entries[key] = src
	for len(c.order) > queryCacheEntries {
		delete(c.entries, c.order[0])
		c.order = c.order[1:]
	}
}

// queryCondition is one filter of where: field op values, or field answered
// (op "") / not answered (op "!").
type queryCondition struct {
	field, op string
	values    []string
}

var queryFieldPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)

// parseQueryWhere reads where: conditions separated by ;, all of which must
// hold. Operators as forms' show-if: f, !f, f=A|B, f!=A, f>N, >=, <, <=.
func parseQueryWhere(text string) ([]queryCondition, error) {
	var out []queryCondition
	for _, part := range strings.Split(text, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "!") && queryFieldPattern.MatchString(strings.TrimSpace(part[1:])) {
			out = append(out, queryCondition{field: strings.TrimSpace(part[1:]), op: "!"})
			continue
		}
		if queryFieldPattern.MatchString(part) {
			out = append(out, queryCondition{field: part})
			continue
		}
		i := strings.IndexAny(part, "=!<>")
		if i <= 0 {
			return nil, invalid(fmt.Sprintf("where: cannot read %q; write field=value, field!=value, field>value (also >=, <, <=), field or !field, separated by ;.", part))
		}
		field := strings.TrimSpace(part[:i])
		rest := part[i:]
		op := ""
		for _, candidate := range []string{"!=", ">=", "<=", "=", ">", "<"} {
			if strings.HasPrefix(rest, candidate) {
				op = candidate
				break
			}
		}
		if op == "" || !queryFieldPattern.MatchString(field) {
			return nil, invalid(fmt.Sprintf("where: cannot read %q.", part))
		}
		value := strings.TrimSpace(rest[len(op):])
		value = strings.Trim(value, `"`)
		values := []string{value}
		if op == "=" || op == "!=" {
			values = strings.Split(value, "|")
		} else if value == "" {
			return nil, invalid(fmt.Sprintf("where: %q compares with nothing.", part))
		}
		out = append(out, queryCondition{field: field, op: op, values: values})
	}
	return out, nil
}

// queryNumber reads a number, a YYYY-MM-DD date or an HH:MM time as a value
// that orders the same way; ok is false for other text.
func queryNumber(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if f, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
		return f, true
	}
	if validDate(s) {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			return float64(t.Unix()) / 86400, true
		}
	}
	if len(s) == 5 && s[2] == ':' {
		if t, err := time.Parse("15:04", s); err == nil {
			return float64(t.Hour()*60 + t.Minute()), true
		}
	}
	return 0, false
}

func (c queryCondition) holds(values []string, me, today string) bool {
	answered := false
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			answered = true
			break
		}
	}
	resolve := func(v string) string {
		switch strings.ToLower(v) {
		case "@me":
			return me
		case "today", "@today":
			return today
		}
		return v
	}
	switch c.op {
	case "":
		return answered
	case "!":
		return !answered
	case "=", "!=":
		match := false
		for _, want := range c.values {
			want = resolve(want)
			for _, v := range values {
				if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(want)) && (want != "" || !answered) {
					match = true
				}
			}
			if want == "" && !answered {
				match = true
			}
		}
		if c.op == "=" {
			return match
		}
		return !match
	default:
		want, ok := queryNumber(resolve(c.values[0]))
		if !ok {
			return false
		}
		for _, v := range values {
			got, ok := queryNumber(v)
			if !ok {
				continue
			}
			switch c.op {
			case ">":
				if got > want {
					return true
				}
			case ">=":
				if got >= want {
					return true
				}
			case "<":
				if got < want {
					return true
				}
			case "<=":
				if got <= want {
					return true
				}
			}
		}
		return false
	}
}

// queryMeasure reads measure: count (default), or sum/avg/min/max FIELD.
func queryMeasure(text string) (kind, field string, err error) {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return "count", "", nil
	}
	kind = strings.ToLower(parts[0])
	switch {
	case kind == "count" && len(parts) == 1:
		return kind, "", nil
	case (kind == "sum" || kind == "avg" || kind == "min" || kind == "max") && len(parts) == 2 && queryFieldPattern.MatchString(parts[1]):
		return kind, parts[1], nil
	}
	return "", "", invalid("measure is count, or sum, avg, min or max followed by a field, such as sum amount.")
}

type measureAcc struct {
	kind   string
	field  string
	count  int
	n      int
	sum    float64
	min    float64
	max    float64
	hasMin bool
}

func (a *measureAcc) add(row queryRow) {
	a.count++
	if a.kind == "count" {
		return
	}
	for _, v := range row.values[a.field] {
		f, ok := queryNumber(v)
		if !ok {
			continue
		}
		a.n++
		a.sum += f
		if !a.hasMin || f < a.min {
			a.min = f
		}
		if !a.hasMin || f > a.max {
			a.max = f
		}
		a.hasMin = true
	}
}

func (a *measureAcc) value() float64 {
	switch a.kind {
	case "sum":
		return a.sum
	case "avg":
		if a.n == 0 {
			return 0
		}
		return a.sum / float64(a.n)
	case "min":
		return a.min
	case "max":
		return a.max
	}
	return float64(a.count)
}

// resolveQuerySource is the source's path in the space: from the space root
// with a leading /, else from the folder of base. A folder ends with /.
func resolveQuerySource(base, source string) (string, bool, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", false, invalid("source is missing: name a folder (jobs/) or a .csv file.")
	}
	folder := strings.HasSuffix(source, "/")
	p := source
	if strings.HasPrefix(source, "/") {
		p = strings.TrimPrefix(source, "/")
	} else {
		dir := ""
		if base != "" {
			if !validPath(base) {
				return "", false, invalid("base must be the dashboard's path.")
			}
			dir = path.Dir(base)
			if dir == "." {
				dir = ""
			}
		}
		p = path.Join(dir, source)
	}
	p = strings.Trim(path.Clean("/"+p), "/")
	if p == "" || p == "." {
		if folder {
			return "", true, nil
		}
		return "", false, invalid("source must be a folder or a .csv file.")
	}
	if !validPath(p) {
		return "", false, invalid("source is not a valid path in this space.")
	}
	if folder {
		return p + "/", true, nil
	}
	if !strings.HasSuffix(strings.ToLower(p), ".csv") {
		return "", false, invalid("source must be a folder (ending in /) or a .csv file.")
	}
	return p, false, nil
}

// query answers one card's query against the space at head. me is the
// viewer's username, for @me.
func (s *Service) query(ctx context.Context, r *repository, space, me string, in QueryInput) (QueryResult, error) {
	if len(in.Source)+len(in.Where)+len(in.Group)+len(in.Measure)+len(in.Sort)+len(in.Columns) > maxQueryText {
		return QueryResult{}, invalid("The query is too long.")
	}
	if in.Limit == 0 {
		in.Limit = 50
	}
	if in.Limit < 1 || in.Limit > maxQueryLimit {
		return QueryResult{}, invalid(fmt.Sprintf("limit must be 1..%d.", maxQueryLimit))
	}
	conditions, err := parseQueryWhere(in.Where)
	if err != nil {
		return QueryResult{}, err
	}
	kind, measureField, err := queryMeasure(in.Measure)
	if err != nil {
		return QueryResult{}, err
	}
	group := strings.TrimSpace(in.Group)
	if group != "" && !queryFieldPattern.MatchString(group) {
		return QueryResult{}, invalid("group is one field name.")
	}
	var columns []string
	for _, c := range strings.Split(in.Columns, ",") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		if !queryFieldPattern.MatchString(c) {
			return QueryResult{}, invalid(fmt.Sprintf("columns: %q is not a field name.", c))
		}
		columns = append(columns, c)
	}
	if len(columns) > maxQueryColumns {
		return QueryResult{}, invalid(fmt.Sprintf("columns takes at most %d fields.", maxQueryColumns))
	}
	sortField, desc := strings.TrimSpace(in.Sort), false
	if strings.HasPrefix(sortField, "-") {
		sortField, desc = strings.TrimSpace(sortField[1:]), true
	}
	if sortField != "" && !queryFieldPattern.MatchString(sortField) {
		return QueryResult{}, invalid("sort is a field, or -field for largest first.")
	}
	today := in.Today
	if today == "" || !validDate(today) {
		today = s.now().UTC().Format("2006-01-02")
	}
	sourcePath, folder, err := resolveQuerySource(in.Base, in.Source)
	if err != nil {
		return QueryResult{}, err
	}
	state, files, err := r.snapshot(ctx, "")
	if err != nil {
		return QueryResult{}, err
	}
	src, err := s.loadQuerySource(ctx, r, files, sourcePath, folder)
	if err != nil {
		return QueryResult{}, err
	}
	result := QueryResult{Space: space, State: state, Source: sourcePath, Kind: src.kind, Fields: src.fields, Measure: kind}
	if measureField != "" {
		result.Measure = kind + " " + measureField
	}
	var matched []queryRow
	for _, row := range src.rows {
		ok := true
		for _, c := range conditions {
			if !c.holds(row.values[c.field], me, today) {
				ok = false
				break
			}
		}
		if ok {
			matched = append(matched, row)
		}
	}
	result.Count = len(matched)
	total := measureAcc{kind: kind, field: measureField}
	for _, row := range matched {
		total.add(row)
	}
	result.Value = total.value()
	if group != "" {
		accs := map[string]*measureAcc{}
		var names []string
		for _, row := range matched {
			keys := row.values[group]
			if len(keys) == 0 || (len(keys) == 1 && strings.TrimSpace(keys[0]) == "") {
				keys = []string{"(none)"}
			}
			for _, k := range keys {
				k = strings.TrimSpace(k)
				a := accs[k]
				if a == nil {
					a = &measureAcc{kind: kind, field: measureField}
					accs[k] = a
					names = append(names, k)
				}
				a.add(row)
			}
		}
		for _, name := range names {
			result.Groups = append(result.Groups, QueryGroup{Group: name, Count: accs[name].count, Value: accs[name].value()})
		}
		// Groups: largest value first by default; sort group names A to Z.
		byName := sortField == "group"
		descending := true
		if sortField == "value" || sortField == "group" {
			descending = desc
		}
		sort.SliceStable(result.Groups, func(i, j int) bool {
			a, b := result.Groups[i], result.Groups[j]
			if byName {
				if descending {
					return compareQueryValues(b.Group, a.Group) < 0
				}
				return compareQueryValues(a.Group, b.Group) < 0
			}
			if a.Value != b.Value {
				if descending {
					return a.Value > b.Value
				}
				return a.Value < b.Value
			}
			return compareQueryValues(a.Group, b.Group) < 0
		})
		if len(result.Groups) > in.Limit {
			result.Groups, result.Truncated = result.Groups[:in.Limit], true
		}
		return result, nil
	}
	if sortField != "" {
		sort.SliceStable(matched, func(i, j int) bool {
			a, b := first(matched[i].values[sortField]), first(matched[j].values[sortField])
			// Rows without the field go last either way.
			if (a == "") != (b == "") {
				return b == ""
			}
			c := compareQueryValues(a, b)
			if desc {
				return c > 0
			}
			return c < 0
		})
	}
	if len(matched) > in.Limit {
		matched, result.Truncated = matched[:in.Limit], true
	}
	result.Rows = make([]QueryRow, 0, len(matched))
	for _, row := range matched {
		out := QueryRow{File: row.file, Line: row.line, Values: map[string]string{}}
		names := columns
		if len(names) == 0 {
			names = src.fields
		}
		for _, name := range names {
			if v, ok := row.values[name]; ok {
				out.Values[name] = strings.Join(v, ", ")
			}
		}
		result.Rows = append(result.Rows, out)
	}
	return result, nil
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

// compareQueryValues orders numbers, dates and times by value and other text
// A to Z without regard to case.
func compareQueryValues(a, b string) int {
	fa, oka := queryNumber(a)
	fb, okb := queryNumber(b)
	if oka && okb {
		switch {
		case fa < fb:
			return -1
		case fa > fb:
			return 1
		}
		return 0
	}
	return strings.Compare(strings.ToLower(a), strings.ToLower(b))
}

// loadQuerySource parses the source's rows, or takes them from the cache
// when its files are unchanged.
func (s *Service) loadQuerySource(ctx context.Context, r *repository, files map[string]record, sourcePath string, folder bool) (*querySource, error) {
	var paths []string
	if folder {
		for p, f := range files {
			if !strings.HasPrefix(p, sourcePath) || strings.Contains(p[len(sourcePath):], "/") || f.Virtual != nil {
				continue
			}
			name := path.Base(p)
			if !strings.HasSuffix(strings.ToLower(name), ".md") || isAgentsFile(p) || strings.HasPrefix(name, ".") {
				continue
			}
			paths = append(paths, p)
		}
		if len(paths) == 0 {
			hasFolder := false
			for p := range files {
				if strings.HasPrefix(p, sourcePath) {
					hasFolder = true
					break
				}
			}
			if !hasFolder {
				return nil, problem(404, "not_found", "The source folder "+sourcePath+" is not in this space.")
			}
		}
		if len(paths) > maxQuerySourceRows {
			return nil, problem(413, "payload_too_large", fmt.Sprintf("%s has more than %d files; a dashboard reads up to %d rows from one source.", sourcePath, maxQuerySourceRows, maxQuerySourceRows))
		}
		sort.Strings(paths)
	} else {
		f, ok := files[sourcePath]
		if !ok {
			return nil, problem(404, "not_found", "The source file "+sourcePath+" is not in this space.")
		}
		if f.Virtual != nil && f.Virtual.TextSHA256 == "" {
			return nil, invalid(sourcePath + " has no text to read.")
		}
		paths = []string{sourcePath}
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00", r.path, sourcePath)
	for _, p := range paths {
		fmt.Fprintf(h, "%s\x00%s\x00", p, files[p].Blob)
	}
	key := hex.EncodeToString(h.Sum(nil))
	if src := s.queryCache.get(key); src != nil {
		return src, nil
	}
	var src *querySource
	if folder {
		hashes := make([]string, 0, len(paths))
		for _, p := range paths {
			hashes = append(hashes, files[p].Blob)
		}
		texts, err := r.blobs(ctx, hashes)
		if err != nil {
			return nil, err
		}
		src = &querySource{kind: "folder"}
		seen := map[string]bool{}
		for _, p := range paths {
			values := markdownRowValues(string(texts[files[p].Blob]))
			name := strings.TrimSuffix(path.Base(p), path.Ext(p))
			values["file"] = []string{p}
			if _, ok := values["name"]; !ok {
				values["name"] = []string{name}
			}
			for k := range values {
				seen[k] = true
			}
			src.rows = append(src.rows, queryRow{file: p, values: values})
		}
		src.fields = sortedQueryFields(seen)
	} else {
		f := files[sourcePath]
		var text string
		if f.Virtual != nil {
			t, err := s.virtualTextOf(ctx, r, sourcePath, *f.Virtual)
			if err != nil {
				return nil, err
			}
			text = t
		} else {
			b, err := r.blob(ctx, f.Blob)
			if err != nil {
				return nil, err
			}
			text = string(b)
		}
		parsed, err := csvRows(sourcePath, text)
		if err != nil {
			return nil, err
		}
		src = parsed
	}
	s.queryCache.put(key, src)
	return src, nil
}

// sortedQueryFields puts file and name first, then the rest A to Z.
func sortedQueryFields(seen map[string]bool) []string {
	fields := make([]string, 0, len(seen))
	for k := range seen {
		if k != "file" && k != "name" {
			fields = append(fields, k)
		}
	}
	sort.Strings(fields)
	return append([]string{"name", "file"}, fields...)
}

// filedCopyKeys are a filed copy's own keys, which keep their meaning when
// an answer has the same name; other answers (title, say) win.
var filedCopyKeys = map[string]bool{"form": true, "template": true, "templateRevision": true, "templateBlob": true, "submission": true, "submittedBy": true, "submittedVia": true, "submittedAt": true, "status": true, "resends": true, "resentAs": true, "quiz": true, "score": true, "passMark": true}

// markdownRowValues reads a Markdown file's front matter as a row. A filed
// form copy also gives each of its answers as a field (see filedCopyKeys).
// Lists ([a, b] or - items) give several values.
func markdownRowValues(text string) map[string][]string {
	values := map[string][]string{}
	keys, _ := splitFrontMatter(text)
	for k, v := range keys {
		if strings.HasPrefix(k, "-") {
			continue
		}
		values[k] = frontMatterList(v)
	}
	// Nested blocks: answers of a filed copy, and "key:" followed by
	// "  - item" lists.
	lines := strings.Split(strings.TrimPrefix(strings.ReplaceAll(text, "\r\n", "\n"), "\ufeff"), "\n")
	if len(lines) < 2 || strings.TrimRight(lines[0], " \t") != "---" {
		return values
	}
	block := ""
	for _, line := range lines[1:] {
		if t := strings.TrimRight(line, " \t"); t == "---" || t == "..." {
			break
		}
		if line == "" {
			continue
		}
		if line[0] != ' ' && line[0] != '\t' {
			block = ""
			if t := strings.TrimSpace(line); strings.HasSuffix(t, ":") {
				block = strings.TrimSuffix(t, ":")
			}
			continue
		}
		if block == "" {
			continue
		}
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "- ") {
			if v := values[block]; len(v) == 1 && v[0] == "" {
				values[block] = nil
			}
			values[block] = append(values[block], unquoteFrontMatter(strings.TrimSpace(t[2:])))
			continue
		}
		if block != "answers" {
			continue
		}
		name, value, ok := strings.Cut(t, ":")
		if !ok || !fieldNamePattern.MatchString(name) {
			continue
		}
		if filedCopyKeys[name] {
			continue
		}
		value = strings.TrimSpace(value)
		var v any
		if json.Unmarshal([]byte(value), &v) != nil {
			v = value
		}
		switch a := v.(type) {
		case string:
			values[name] = []string{a}
		case []any:
			list := []string{}
			for _, item := range a {
				list = append(list, fmt.Sprint(item))
			}
			values[name] = list
		case nil:
			values[name] = []string{""}
		default:
			values[name] = []string{fmt.Sprint(a)}
		}
	}
	// A key whose value is empty and holds a nested block is not a field.
	delete(values, "answers")
	return values
}

func frontMatterList(v string) []string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '[' && v[len(v)-1] == ']' {
		var list []string
		for _, item := range strings.Split(v[1:len(v)-1], ",") {
			if item = unquoteFrontMatter(strings.TrimSpace(item)); item != "" {
				list = append(list, item)
			}
		}
		return list
	}
	return []string{v}
}

func unquoteFrontMatter(v string) string {
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		var s string
		if json.Unmarshal([]byte(v), &s) == nil {
			return s
		}
	}
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
	}
	return v
}

// csvRows reads a CSV file whose first line names the fields.
func csvRows(sourcePath, text string) (*querySource, error) {
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(text, "\ufeff")))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	records, err := reader.ReadAll()
	if err != nil {
		return nil, invalid(fmt.Sprintf("%s is not a CSV file Metatrash can read: %v.", sourcePath, err))
	}
	if len(records) == 0 {
		return &querySource{kind: "csv", fields: []string{}}, nil
	}
	if len(records)-1 > maxQuerySourceRows {
		return nil, problem(413, "payload_too_large", fmt.Sprintf("%s has more than %d rows; a dashboard reads up to %d rows from one source.", sourcePath, maxQuerySourceRows, maxQuerySourceRows))
	}
	header := records[0]
	fields := make([]string, 0, len(header))
	for i, name := range header {
		name = strings.TrimSpace(name)
		if name == "" {
			name = fmt.Sprintf("column%d", i+1)
		}
		fields = append(fields, name)
	}
	src := &querySource{kind: "csv", fields: fields}
	for i, rec := range records[1:] {
		values := map[string][]string{}
		blank := true
		for j, name := range fields {
			v := ""
			if j < len(rec) {
				v = strings.TrimSpace(rec[j])
			}
			if v != "" {
				blank = false
			}
			values[name] = []string{v}
		}
		if blank {
			continue
		}
		src.rows = append(src.rows, queryRow{file: sourcePath, line: i + 2, values: values})
	}
	return src, nil
}
