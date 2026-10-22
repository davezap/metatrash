package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The policies answer is cut to the synced folder, and ack names the file in
// the space.
func TestPoliciesAndAck(t *testing.T) {
	var posted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/account/spaces/dave/work/acknowledgements" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &posted)
			_, _ = io.WriteString(w, `{"id":"f1","path":"hr/policies/leave.md","publishedPath":"hr/policies/leave.md","tracked":true}`)
			return
		}
		_, _ = io.WriteString(w, `{"space":"dave/work","state":"s","outstanding":2,"folders":[
			{"folder":"hr/policies/","version":3,"revision":"r","timestamp":"2026-10-22T10:00:00Z","acknowledge":true,"files":[
				{"id":"f1","path":"hr/policies/leave.md","publishedPath":"hr/policies/leave.md","tracked":true,"outstanding":true}]},
			{"folder":"it/","version":1,"revision":"r2","timestamp":"2026-10-22T10:00:00Z","acknowledge":true,"files":[
				{"id":"f2","path":"it/laptops.md","publishedPath":"it/laptops.md","tracked":true,"outstanding":true}]}]}`)
	}))
	defer server.Close()
	c := newClient(server.URL, "dave/work/hr", func(context.Context, bool) (string, error) { return "t", nil }, true)
	got, err := c.policies(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Outstanding != 1 || len(got.Folders) != 1 || got.Folders[0].Folder != "policies/" || got.Folders[0].Files[0].Path != "policies/leave.md" {
		t.Fatalf("policies: %+v", got)
	}
	f, err := c.acknowledge(context.Background(), "policies/leave.md", 3)
	if err != nil || f.Path != "policies/leave.md" || posted["path"] != "hr/policies/leave.md" || posted["version"] != float64(3) {
		t.Fatalf("ack: %+v %v %v", f, posted, err)
	}
}
