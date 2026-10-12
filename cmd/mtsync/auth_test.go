package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestLoginAndRefresh runs login against a fake authorization server that
// approves at once, then refreshes the rotating token pair.
func TestLoginAndRefresh(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	var challenge, redirect string
	refreshes := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/authorize":
			q := r.URL.Query()
			if q.Get("client_id") != srv.URL+"/oauth/clients/sync" || q.Get("resource") != srv.URL+"/api/v1/account" || q.Get("code_challenge_method") != "S256" {
				t.Errorf("authorize query: %v", q)
			}
			challenge, redirect = q.Get("code_challenge"), q.Get("redirect_uri")
			http.Redirect(w, r, redirect+"?"+url.Values{"code": {"c1"}, "state": {q.Get("state")}, "iss": {srv.URL}}.Encode(), http.StatusSeeOther)
		case "/oauth/token":
			_ = r.ParseForm()
			switch r.PostForm.Get("grant_type") {
			case "authorization_code":
				sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
				if r.PostForm.Get("code") != "c1" || r.PostForm.Get("redirect_uri") != redirect || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
					w.WriteHeader(400)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "a1", "refresh_token": "r1", "expires_in": 3600})
			case "refresh_token":
				if r.PostForm.Get("refresh_token") != "r1" {
					w.WriteHeader(400)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
					return
				}
				refreshes++
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "a2", "refresh_token": "r2", "expires_in": 3600})
			}
		}
	}))
	defer srv.Close()
	openBrowser = func(target string) {
		go func() {
			resp, err := http.Get(target)
			if err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
	}
	ctx := context.Background()
	if err := login(ctx, srv.URL, io.Discard); err != nil {
		t.Fatal(err)
	}
	token := tokenSource(srv.URL)
	if access, err := token(ctx, false); err != nil || access != "a1" {
		t.Fatalf("stored token: %q %v", access, err)
	}
	// A refused token refreshes once; the rotated pair is saved.
	if access, err := token(ctx, true); err != nil || access != "a2" {
		t.Fatalf("refreshed token: %q %v", access, err)
	}
	all, _ := loadAllCredentials()
	if all[srv.URL].Refresh != "r2" || refreshes != 1 {
		t.Fatalf("saved credentials: %+v", all[srv.URL])
	}
	// Another process holding the newer token does not refresh again.
	if access, err := tokenSource(srv.URL)(ctx, true); err != nil || access != "a2" || refreshes != 1 {
		t.Fatalf("second process: %q %v refreshes %d", access, err, refreshes)
	}
	if err := saveCredentials(srv.URL, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := token(ctx, false); err != errSignedOut {
		t.Fatalf("after logout: %v", err)
	}
}
