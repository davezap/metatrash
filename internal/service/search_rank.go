package service

// Best matches (0.97.0): search with rank set treats the query as words, any
// of which may match the start of a word in a file, and answers with files in
// order of how well they match, each with its best few lines. Scores are
// worked out here from the files' text, the same way whether or not the
// search index is on: the index only chooses which files to read, so a server
// without it gives the same answer, more slowly.

import (
	"context"
	"math"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis"
	"github.com/blevesearch/bleve/v2/search/query"
)

const (
	rankDefaultLimit = 10
	rankMaxLimit     = 50
	rankMaxWords     = 10
	rankLinesPerFile = 3
	// rankMaxSpaces is how many spaces one search of every space covers.
	rankMaxSpaces = 50
	// rankParallel is how many spaces such a search reads at once.
	rankParallel = 4
)

// RankedLine is one of a file's best lines.
type RankedLine struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

// RankedFile is one file in a ranked answer.
type RankedFile struct {
	Space string       `json:"space,omitempty"`
	Path  string       `json:"path"`
	ID    string       `json:"id"`
	Score float64      `json:"score"`
	Lines []RankedLine `json:"lines"`
}

// RankedResult answers a search with rank. State is the space's snapshot;
// a search of every space gives States (space to snapshot) instead, and
// names in Skipped the spaces it left out.
type RankedResult struct {
	Space     string            `json:"space"`
	State     string            `json:"state,omitempty"`
	States    map[string]string `json:"states,omitempty"`
	Files     []RankedFile      `json:"files"`
	Truncated bool              `json:"truncated"`
	Skipped   []string          `json:"skipped,omitempty"`
}

// rankAnalyzer splits text into lower-case words exactly as the index does.
var rankAnalyzer = sync.OnceValue(func() analysis.Analyzer {
	return searchMapping().AnalyzerNamed(searchIndexAnalyzer)
})

func rankTokens(text string) []string {
	toks := rankAnalyzer().Analyze([]byte(text))
	out := make([]string, len(toks))
	for i, t := range toks {
		out[i] = string(t.Term)
	}
	return out
}

// checkRankInput checks a ranked search and returns its words.
func checkRankInput(in *Input) ([]string, error) {
	if in.Query == "" || len(in.Query) > maxSearchQuery || strings.ContainsAny(in.Query, "\n\x00") {
		return nil, invalid("query takes 1 to 500 characters on one line.")
	}
	if in.Regex || in.Revision != "" || in.Context != 0 {
		return nil, invalid("rank can't be combined with regex, revision or context; search without rank for exact lines.")
	}
	if in.Limit == 0 {
		in.Limit = rankDefaultLimit
	}
	if in.Limit < 1 || in.Limit > rankMaxLimit {
		return nil, invalid("with rank, limit is the number of files: 1..50.")
	}
	seen := map[string]bool{}
	words := []string{}
	for _, w := range rankTokens(in.Query) {
		if !seen[w] && len(words) < rankMaxWords {
			seen[w] = true
			words = append(words, w)
		}
	}
	if len(words) == 0 {
		return nil, invalid("query has no words to rank by; search without rank for symbols.")
	}
	return words, nil
}

// rankFile is what scoring needs about one file.
type rankFile struct {
	path, id string
	text     string
	tf       []int
	name     []bool
	folder   []bool
}

func hasWordPrefix(tokens []string, w string) bool {
	for _, t := range tokens {
		if strings.HasPrefix(t, w) {
			return true
		}
	}
	return false
}

// rankedSearch is search with rank, in one space at its head.
func (s *Service) rankedSearch(ctx context.Context, r *repository, space string, in Input) (RankedResult, error) {
	if in.Prefix != "" && (!strings.HasSuffix(in.Prefix, "/") || !validPath(strings.TrimSuffix(in.Prefix, "/")) || strings.Count(in.Prefix, "/") > 7) {
		return RankedResult{}, invalid("Invalid folder prefix.")
	}
	words, err := checkRankInput(&in)
	if err != nil {
		return RankedResult{}, err
	}
	state, files, err := r.snapshot(ctx, "")
	if err != nil {
		return RankedResult{}, err
	}
	docling := s.docling()
	paths := []string{}
	for p, f := range files {
		if strings.HasPrefix(p, in.Prefix) && f.Record == nil && (f.Virtual == nil || f.Virtual.TextSHA256 != "" || docling.wants(r, *f.Virtual)) {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	total := len(paths)
	// A file can match by its name or folder alone, which the index doesn't
	// hold, so those are always read.
	names := make(map[string][2][]string, len(paths))
	byName := map[string]bool{}
	for _, p := range paths {
		n := [2][]string{rankTokens(path.Base(p)), rankTokens(path.Dir(p))}
		if path.Dir(p) == "." {
			n[1] = nil
		}
		names[p] = n
		for _, w := range words {
			if hasWordPrefix(n[0], w) || hasWordPrefix(n[1], w) {
				byName[p] = true
				break
			}
		}
	}
	if si := s.searchIdx.acquire(r); si != nil {
		keys := make(map[string]string, len(paths))
		for _, p := range paths {
			keys[p] = s.searchKey(r, files[p])
		}
		if narrowed, ok := s.searchIdx.anyCandidates(r, si, words, paths, keys); ok {
			in := map[string]bool{}
			for _, p := range narrowed {
				in[p] = true
			}
			kept := paths[:0:0]
			for _, p := range paths {
				if in[p] || byName[p] {
					kept = append(kept, p)
				}
			}
			paths = kept
		}
		s.searchIdx.release(si)
	}
	hashes := make([]string, 0, len(paths))
	for _, p := range paths {
		if files[p].Virtual == nil {
			hashes = append(hashes, files[p].Blob)
		}
	}
	texts, err := r.blobs(ctx, hashes)
	if err != nil {
		return RankedResult{}, err
	}
	df := make([]int, len(words))
	scored := []rankFile{}
	for _, p := range paths {
		f := files[p]
		text := string(texts[f.Blob])
		if f.Virtual != nil {
			if text, err = s.virtualTextOf(ctx, r, p, *f.Virtual); err != nil {
				continue
			}
		}
		rf := rankFile{path: p, id: f.ID, text: text, tf: make([]int, len(words)), name: make([]bool, len(words)), folder: make([]bool, len(words))}
		any := false
		for _, t := range rankTokens(text) {
			for i, w := range words {
				if strings.HasPrefix(t, w) {
					rf.tf[i]++
				}
			}
		}
		n := names[p]
		for i, w := range words {
			rf.name[i], rf.folder[i] = hasWordPrefix(n[0], w), hasWordPrefix(n[1], w)
			if rf.tf[i] > 0 || rf.name[i] || rf.folder[i] {
				df[i]++
				any = true
			}
		}
		if any {
			scored = append(scored, rf)
		}
	}
	type ranked struct {
		f     *rankFile
		score float64
	}
	out := make([]ranked, 0, len(scored))
	for i := range scored {
		out = append(out, ranked{&scored[i], rankScore(&scored[i], df, total)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].f.path < out[j].f.path
	})
	result := RankedResult{Space: space, State: state, Files: []RankedFile{}}
	if len(out) > in.Limit {
		out, result.Truncated = out[:in.Limit], true
	}
	for _, o := range out {
		result.Files = append(result.Files, RankedFile{Path: o.f.path, ID: o.f.id, Score: math.Round(o.score*1000) / 1000, Lines: bestLines(o.f.text, words)})
	}
	return result, nil
}

// rankScore weighs each word by how rare it is in the space (BM25's idf),
// counts its uses with diminishing returns, adds a bonus for the file's name
// and folder, and favours files that hold more of the words.
func rankScore(f *rankFile, df []int, total int) float64 {
	score, held := 0.0, 0
	for i := range df {
		idf := math.Log(1 + (float64(total)-float64(df[i])+0.5)/(float64(df[i])+0.5))
		w := 0.0
		if tf := float64(f.tf[i]); tf > 0 {
			w += tf / (tf + 1.2)
		}
		if f.name[i] {
			w += 1.5
		} else if f.folder[i] {
			w += 0.5
		}
		if w > 0 {
			held++
		}
		score += idf * w
	}
	return score * (0.5 + 0.5*float64(held)/float64(len(df)))
}

// bestLines picks the lines holding the most of the words (the first of
// equals), at most rankLinesPerFile, in file order.
func bestLines(text string, words []string) []RankedLine {
	type cand struct{ line, n int }
	var cands []cand
	lines := splitLines(text)
	for i, line := range lines {
		low := strings.ToLower(line)
		maybe := false
		for _, w := range words {
			if strings.Contains(low, w) {
				maybe = true
				break
			}
		}
		if !maybe {
			continue
		}
		toks := rankTokens(line)
		n := 0
		for _, w := range words {
			if hasWordPrefix(toks, w) {
				n++
			}
		}
		if n > 0 {
			cands = append(cands, cand{i, n})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].n > cands[j].n })
	if len(cands) > rankLinesPerFile {
		cands = cands[:rankLinesPerFile]
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].line < cands[j].line })
	out := []RankedLine{}
	for _, c := range cands {
		out = append(out, RankedLine{Line: c.line + 1, Text: clipLine(trimLineEnd(lines[c.line]))})
	}
	return out
}

// anyCandidates names the files that may hold any of words (as the start of
// a word): those the index says do, and those it hasn't caught up with. It
// answers ok false when it can't tell (a word starting too many words).
func (m *searchIndexes) anyCandidates(r *repository, si *spaceIndex, words []string, paths []string, keys map[string]string) (out []string, ok bool) {
	view := si.view.Load()
	var ds []query.Query
	for _, w := range words {
		terms := expandTerms(view.terms, w, func(string) bool { return true })
		if terms == nil {
			return nil, false
		}
		for _, t := range terms {
			tq := bleve.NewTermQuery(t)
			tq.SetField("body")
			ds = append(ds, tq)
		}
	}
	hit := map[string]bool{}
	if count, _ := si.idx.DocCount(); count > 0 && len(ds) > 0 {
		req := bleve.NewSearchRequestOptions(bleve.NewDisjunctionQuery(ds...), int(count), 0, false)
		req.Score = "none"
		res, err := si.idx.Search(req)
		if err != nil {
			return nil, false
		}
		for _, h := range res.Hits {
			hit[h.ID] = true
		}
	}
	behind := false
	for _, p := range paths {
		if view.keys[p] == keys[p] {
			if hit[p] {
				out = append(out, p)
			}
			continue
		}
		out = append(out, p)
		if !si.recentlySkipped(p, keys[p]) {
			behind = true
		}
	}
	if behind {
		m.queue(r)
	}
	m.narrowed.Add(1)
	return out, true
}

// rankedSearchAll is a ranked search of every space an account connection
// may read, at most rankMaxSpaces of them, merged by score.
func (s *Service) rankedSearchAll(ctx context.Context, id oauthIdentity, client string, in Input) (RankedResult, error) {
	if in.Prefix != "" {
		return RankedResult{}, invalid("prefix needs one space; leave it out when space is \"*\".")
	}
	if _, err := checkRankInput(&in); err != nil {
		return RankedResult{}, err
	}
	connected, err := s.listConnectedSpaces(ctx, id)
	if err != nil {
		return RankedResult{}, err
	}
	result := RankedResult{Space: "*", States: map[string]string{}, Files: []RankedFile{}}
	spaces := []string{}
	for _, c := range connected.Spaces {
		if len(spaces) == rankMaxSpaces {
			result.Skipped = append(result.Skipped, c.Space)
			continue
		}
		spaces = append(spaces, c.Space)
	}
	type answer struct {
		res RankedResult
		err error
	}
	answers := make([]answer, len(spaces))
	sem := make(chan struct{}, rankParallel)
	var wg sync.WaitGroup
	for i, sp := range spaces {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			repo, name, err := s.agentSpaceAccess(ctx, id, sp, client, false)
			if err == nil {
				answers[i].res, err = s.rankedSearch(ctx, repo, name, in)
			}
			answers[i].err = err
		}()
	}
	wg.Wait()
	for i, a := range answers {
		if a.err != nil {
			result.Skipped = append(result.Skipped, spaces[i])
			continue
		}
		result.States[a.res.Space] = a.res.State
		result.Truncated = result.Truncated || a.res.Truncated
		for _, f := range a.res.Files {
			f.Space = a.res.Space
			result.Files = append(result.Files, f)
		}
	}
	sort.SliceStable(result.Files, func(i, j int) bool {
		a, b := result.Files[i], result.Files[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Space != b.Space {
			return a.Space < b.Space
		}
		return a.Path < b.Path
	})
	if len(result.Files) > in.Limit {
		result.Files, result.Truncated = result.Files[:in.Limit], true
	}
	return result, nil
}
