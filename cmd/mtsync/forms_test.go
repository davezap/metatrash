package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// submit names the form in the space with its text's blob hash; a refusal
// carries each field's problem; receipts are cut to the synced folder.
func TestSubmitAndReceipts(t *testing.T) {
	if got := gitBlobHash([]byte("hello\n")); got != "ce013625030ba8dba906f756967f9e9ca394464a" {
		t.Fatalf("blob hash %s", got)
	}
	var posted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/account/spaces/dave/work/submissions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &posted)
			if posted["answers"].(map[string]any)["name"] == "" {
				w.WriteHeader(400)
				_, _ = io.WriteString(w, `{"error":{"code":"invalid_answers","message":"Some answers need fixing.","fields":[{"name":"name","message":"This answer is required."}]}}`)
				return
			}
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"id":"r1","form":"leave","title":"Leave","template":"hr/forms/leave.md","target":"dave/review/leave/x.md","submittedAt":"2026-10-23T09:14:00Z","text":"copy"}`)
		case r.URL.Query().Get("id") == "r1":
			_, _ = io.WriteString(w, `{"id":"r1","template":"hr/forms/leave.md","text":"copy"}`)
		default:
			_, _ = io.WriteString(w, `{"space":"dave/work","submissions":[{"id":"r1","template":"hr/forms/leave.md"}]}`)
		}
	}))
	defer server.Close()
	c := newClient(server.URL, "dave/work/hr", func(context.Context, bool) (string, error) { return "t", nil }, true)
	r, err := c.submit(context.Background(), "forms/leave.md", "abc", json.RawMessage(`{"name":"Pat"}`))
	if err != nil || r.Template != "forms/leave.md" || posted["path"] != "hr/forms/leave.md" || posted["blob"] != "abc" || posted["today"] == "" {
		t.Fatalf("submit: %+v %v %v", r, posted, err)
	}
	_, err = c.submit(context.Background(), "forms/leave.md", "abc", json.RawMessage(`{"name":""}`))
	var refused *apiError
	if !errors.As(err, &refused) || refused.Status != 400 || len(refused.Fields) != 1 || refused.Fields[0].Name != "name" {
		t.Fatalf("refused: %v", err)
	}
	list, err := c.receipts(context.Background(), "")
	if err != nil || len(list.Submissions) != 1 || list.Submissions[0].Template != "forms/leave.md" {
		t.Fatalf("receipts: %+v %v", list, err)
	}
	one, err := c.receipts(context.Background(), "r1")
	if err != nil || one.Submissions[0].Text != "copy" {
		t.Fatalf("receipt: %+v %v", one, err)
	}
	if _, err := readAnswers("-", strings.NewReader(`["x"]`)); err == nil {
		t.Fatal("answers must be an object")
	}
	if b, err := readAnswers("-", strings.NewReader(`{"a":"b"}`)); err != nil || string(b) != `{"a":"b"}` {
		t.Fatalf("answers: %s %v", b, err)
	}
}
