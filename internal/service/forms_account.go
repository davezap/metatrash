package service

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"sync"
)

// Forms across all of a person's spaces (0.95.22), for the website's account
// menu and My Submissions:
//
//	GET /api/v1/site/submissions[?id=ID]  every form the person sent, newest first
//	GET /api/v1/site/todo                 per space, what waits for them
//
// Both read only the signed-in person's own rows and the spaces they can
// open now (owned, or joined and active), so neither shows anything a
// per-space call would not.

// maxAccountSpaces caps the spaces one account-wide answer opens.
const maxAccountSpaces = 40

// accountReceipt is a receipt in the account-wide list.
type accountReceipt struct {
	Receipt
	// Space is the space the form is in, as owner/slug.
	Space string `json:"space,omitempty"`
	// Current is the form's path in that space now, when the person can
	// still open it; SameForm is set when its text there is the text the
	// answers were given to, so they can start a new copy from them.
	Current  string `json:"current,omitempty"`
	SameForm bool   `json:"sameForm,omitempty"`
}

type accountReceiptsResult struct {
	Submissions []accountReceipt `json:"submissions"`
}

// todoSpace is one space with something waiting for the person.
type todoSpace struct {
	Space string `json:"space"`
	Name  string `json:"name"`
	// ToRead counts the files and quizzes to confirm, ToReview the copies
	// waiting for a review (only where they can write), SentBack the forms
	// a reviewer sent back for more information.
	ToRead   int `json:"toRead"`
	ToReview int `json:"toReview"`
	SentBack int `json:"sentBack"`
}

type todoResult struct {
	Total  int         `json:"total"`
	Spaces []todoSpace `json:"spaces"`
	// Partial is set when some spaces could not be checked just now.
	Partial bool `json:"partial,omitempty"`
}

// serveSiteForms answers the account-wide forms routes of the site API.
func (h *httpAdapter) serveSiteForms(w http.ResponseWriter, r *http.Request, user userAccount, signedIn bool, client, rest string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET")
		sendError(w, problem(405, "invalid_request", "Use GET."))
		return
	}
	if !signedIn {
		sendError(w, problem(401, "unauthorized", "Sign in to see your forms."))
		return
	}
	if rest == "todo" {
		if r.URL.RawQuery != "" {
			sendError(w, invalid("Unknown query parameter."))
			return
		}
		value, err := h.service.accountTodo(r.Context(), user, client)
		if err != nil {
			sendError(w, err)
			return
		}
		sendJSON(w, 200, value)
		return
	}
	id := ""
	if r.URL.RawQuery != "" {
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(q) != 1 || len(q["id"]) != 1 || !receiptIDPattern.MatchString(q["id"][0]) {
			sendError(w, invalid("Unknown query parameter; the only one is id=ID."))
			return
		}
		id = q["id"][0]
	}
	rows, err := h.service.accountSubmissions(r.Context(), user, client, id)
	if err != nil {
		sendError(w, err)
		return
	}
	if id != "" {
		if len(rows) == 0 {
			sendError(w, missing())
			return
		}
		sendJSON(w, 200, rows[0])
		return
	}
	sendJSON(w, 200, accountReceiptsResult{Submissions: rows})
}

// accountSubmissions lists a person's receipts from every space, each with
// the form's space and whether the form there is still the one they filled
// in.
func (s *Service) accountSubmissions(ctx context.Context, user userAccount, client, id string) ([]accountReceipt, error) {
	rows, err := s.ownedDB.submissionRows(ctx, "", user.ID, id)
	if err != nil {
		return nil, err
	}
	spaces, err := s.ownedDB.accessibleSpaces(ctx, user)
	if err != nil {
		return nil, problem(503, "unavailable", "Your spaces are temporarily unavailable.")
	}
	open := map[string]bool{}
	for _, sp := range spaces {
		// A space an extension holds back (SpaceGate) isn't open now.
		open[sp.ID] = s.ownedDB.gateFor(ctx, user.ID, sp.OwnerID) == nil
	}
	// The forms' files now, per space the person can still open.
	type formFile struct{ path, blob, published string }
	files := map[string]map[string]formFile{}
	for _, row := range rows {
		if !open[row.spaceID] || files[row.spaceID] != nil || len(files) >= maxAccountSpaces {
			continue
		}
		byID := map[string]formFile{}
		files[row.spaceID] = byID
		repo := s.ownedRepository(row.spaceID)
		if repo == nil || s.reserveOwnedOperation(row.spaceID, client, false) != nil {
			continue
		}
		state, snapshot, err := repo.snapshot(ctx, "")
		if err != nil {
			continue
		}
		for p, f := range snapshot {
			byID[f.ID] = formFile{path: p, blob: f.Blob}
		}
		// In a published folder the form people fill in is its published
		// text, which is what was recorded.
		if folders, err := repo.publishedFolders(ctx, state, snapshot); err == nil {
			for _, folder := range folders {
				for _, pf := range folder.Files {
					if f, ok := byID[pf.ID]; ok {
						f.published = pf.Blob
						byID[pf.ID] = f
					}
				}
			}
		}
	}
	out := make([]accountReceipt, 0, len(rows))
	for _, row := range rows {
		item := accountReceipt{Receipt: row.Receipt, Space: row.spaceName}
		if f, ok := files[row.spaceID][row.fileID]; ok && row.fileID != "" {
			item.Current = f.path
			item.SameForm = row.blob != "" && (f.blob == row.blob || f.published == row.blob)
		}
		out = append(out, item)
	}
	return out, nil
}

// accountTodo counts what waits for the person in each space they can open:
// files to read, copies to review and forms sent back to them.
func (s *Service) accountTodo(ctx context.Context, user userAccount, client string) (todoResult, error) {
	spaces, err := s.ownedDB.accessibleSpaces(ctx, user)
	if err != nil {
		return todoResult{}, problem(503, "unavailable", "Your spaces are temporarily unavailable.")
	}
	out := todoResult{Spaces: []todoSpace{}}
	if len(spaces) > maxAccountSpaces {
		spaces, out.Partial = spaces[:maxAccountSpaces], true
	}
	found := make([]*todoSpace, len(spaces))
	failed := make([]bool, len(spaces))
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for i, sp := range spaces {
		repo := s.ownedRepository(sp.ID)
		if repo == nil || sp.Owner == "" || s.ownedDB.gateFor(ctx, user.ID, sp.OwnerID) != nil {
			continue
		}
		if s.reserveOwnedOperation(sp.ID, client, false) != nil {
			failed[i] = true
			continue
		}
		wg.Add(1)
		go func(i int, sp accessibleSpace, repo *repository) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			name := sp.Owner + "/" + sp.Slug
			result, err := s.policies(ctx, repo, name, user.ID, sp.CanWrite)
			if err != nil {
				failed[i] = true
				return
			}
			item := todoSpace{Space: name, Name: sp.Name, ToRead: result.Outstanding, ToReview: len(result.ToReview), SentBack: len(result.ToFix)}
			if item.ToRead+item.ToReview+item.SentBack > 0 {
				found[i] = &item
			}
		}(i, sp, repo)
	}
	wg.Wait()
	for i, item := range found {
		if failed[i] {
			out.Partial = true
		}
		if item != nil {
			out.Spaces = append(out.Spaces, *item)
			out.Total += item.ToRead + item.ToReview + item.SentBack
		}
	}
	sort.SliceStable(out.Spaces, func(a, b int) bool { return out.Spaces[a].Space < out.Spaces[b].Space })
	return out, nil
}
