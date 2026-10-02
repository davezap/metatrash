package service

import (
	"net/url"
	"strings"
	"testing"
)

func TestDeriveSpaceSlug(t *testing.T) {
	for name, want := range map[string]string{
		"bartco":                    "bartco",
		"Bartco Notes":              "bartco-notes",
		"  Bartco   Notes!  ":       "bartco-notes",
		"Dave's notes":              "daves-notes",
		"Dave’s notes":              "daves-notes",
		"Ōtautahi Māori Wānanga":    "otautahi-maori-wananga",
		"Café Zürich":               "cafe-zurich",
		"Q3/Q4 plans (draft)":       "q3-q4-plans-draft",
		"2026":                      "2026",
		"--a--b--":                  "a-b",
		"Straße":                    "strasse",
		"🙂 rocket 🚀 notes":          "rocket-notes",
		strings.Repeat("word ", 20): "word-word-word-word-word-word-word-word-word",
		strings.Repeat("x", 60):     strings.Repeat("x", 48),
	} {
		got, err := deriveSpaceSlug(name)
		if err != nil || got != want {
			t.Errorf("deriveSpaceSlug(%q) = %q, %v; want %q", name, got, err, want)
		}
		if err == nil {
			if _, slug, err := normalizeSpaceName("x", got); err != nil || slug != got {
				t.Errorf("derived slug %q is not a valid slug: %v", got, err)
			}
		}
	}
	for _, name := range []string{"", "   ", "🙂🚀", "東京", "'’"} {
		if got, err := deriveSpaceSlug(name); err == nil {
			t.Errorf("deriveSpaceSlug(%q) = %q, want an error", name, got)
		}
	}
}

func TestCreateSpaceFormAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	maker := w.user("maker")
	session := w.session(maker)
	csrf := w.s.accounts.mac("space-create:" + session.Value)
	create := func(fields url.Values) (int, string, string) {
		fields.Set("csrf", csrf)
		res := oauthCall(w.h, "POST", "/account/spaces", fields, session)
		return res.Code, res.Header().Get("Location"), res.Body.String()
	}
	page := oauthCall(w.h, "GET", "/account", nil, session).Body.String()
	if strings.Contains(page, `name="slug"`) || !strings.Contains(page, "/spaces/"+maker.Username+"/bartco-notes") {
		t.Fatal("create form still asks for a slug, or the address example is missing")
	}
	// Name problems are reported on the name field.
	for name, message := range map[string]string{
		"🙂🚀":                     "Use at least one letter or number in the name",
		strings.Repeat("n", 121): "Use a space name of 1–120 characters",
	} {
		code, _, body := create(url.Values{"name": {name}})
		if code != 400 || !strings.Contains(body, `aria-invalid="true"`) || !strings.Contains(body, `id="space-name-error"`) || !strings.Contains(body, message) {
			t.Fatalf("name %q: %d %s", name, code, body)
		}
	}
	// One field: the slug is derived and the browser lands on the new space.
	code, location, _ := create(url.Values{"name": {"Bartco Notes!"}})
	if code != 303 || location != "/spaces/"+maker.Username+"/bartco-notes/" {
		t.Fatalf("create: %d %q", code, location)
	}
	spaces, err := w.h.accountSpaces(t.Context(), maker)
	if err != nil || len(spaces) != 1 || spaces[0].Name != "Bartco Notes!" || spaces[0].Slug != "bartco-notes" {
		t.Fatalf("stored space: %v %v", spaces, err)
	}
	// A name that gives an address already in use is refused, even an identical
	// name (which a retry would otherwise reuse) and even at the allowance limit.
	for _, name := range []string{"Bartco Notes!", "bartco notes"} {
		code, _, body := create(url.Values{"name": {name}})
		if code != 409 || !strings.Contains(body, "You already have a space at /spaces/"+maker.Username+"/bartco-notes. Choose a different name.") {
			t.Fatalf("duplicate %q: %d %s", name, code, body)
		}
	}
	// Retry preparation still sends the stored slug and finishes idempotently.
	if code, location, _ := create(url.Values{"name": {"Bartco Notes!"}, "slug": {"bartco-notes"}}); code != 303 || location != "/spaces/"+maker.Username+"/bartco-notes/" {
		t.Fatalf("retry: %d %q", code, location)
	}
	if spaces, _ := w.h.accountSpaces(t.Context(), maker); len(spaces) != 1 {
		t.Fatalf("retry created another space: %v", spaces)
	}
}
