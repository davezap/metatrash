package service

import (
	"context"
	"sort"
	"strings"
	"testing"
)

func TestParseActions(t *testing.T) {
	good := []string{
		`{"actions":[]}`,
		`{"actions":[{"name":"tidy-draft","on":"inbound/*.md","do":"Tidy it."}]}`,
		`{"actions":[{"name":"convert","on":"inbound/*.pdf, inbound/*.docx","do":"Convert it.","web":false,"steps":200}]}`,
		`{"actions":[{"name":"a","on":["*.md","drafts/"],"when":"change","do":"instructions.md"}]}`,
		`{"actions":[{"name":"a","on":"x.md","do":"a"},{"name":"b","on":"y.md","do":"b"}],"locked":{"actions":"agents"}}`,
	}
	for _, text := range good {
		if _, err := parseFolderConfig(text); err != nil {
			t.Errorf("%s: %v", text, err)
		}
	}
	bad := map[string]string{
		`{"actions":{}}`:                                                                 "wrong value type",
		`{"actions":[{"on":"x.md","do":"y"}]}`:                                           "needs a name",
		`{"actions":[{"name":"Tidy Draft","on":"x.md","do":"y"}]}`:                       "needs a name",
		`{"actions":[{"name":"a","do":"y"}]}`:                                            `say when it runs`,
		`{"actions":[{"name":"a","on":"x.md"}]}`:                                         "do says what to do",
		`{"actions":[{"name":"a","on":"x.md","do":"  "}]}`:                               "do says what to do",
		`{"actions":[{"name":"a","on":"x.md","every":"hour","do":"y"}]}`:                 "not both",
		`{"actions":[{"name":"a","on":"../x.md","do":"y"}]}`:                             "relative to this folder",
		`{"actions":[{"name":"a","on":"/x.md","do":"y"}]}`:                               "relative to this folder",
		`{"actions":[{"name":"a","on":"a, ,b","do":"y"}]}`:                               "relative to this folder",
		`{"actions":[{"name":"a","on":"**/*.md","do":"y"}]}`:                             "relative to this folder",
		`{"actions":[{"name":"a","on":"[.md","do":"y"}]}`:                                "relative to this folder",
		`{"actions":[{"name":"a","on":"x.md","when":"edit","do":"y"}]}`:                  `when is "arrive"`,
		`{"actions":[{"name":"a","on":"x.md","do":"y","steps":201}]}`:                    "steps",
		`{"actions":[{"name":"a","on":"x.md","do":"y","shell":"ls"}]}`:                   "unknown",
		`{"actions":[{"name":"a","on":"x.md","do":"y"},{"name":"a","on":"z","do":"w"}]}`: "unique",
	}
	for text, want := range bad {
		_, err := parseFolderConfig(text)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want error containing %q", text, err, want)
		}
	}
	if _, err := parseFolderConfig(`{"actions":[{"name":"a","on":"x.md","do":"` + strings.Repeat("ā", maxActionDo+1) + `"}]}`); err == nil {
		t.Error("accepted over-long instructions")
	}
}

func TestActionMatchesAndDoFile(t *testing.T) {
	c, err := parseFolderConfig(`{"actions":[{"name":"a","on":"inbound/*.md, notes.txt, drafts/","do":"steps.md"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	a := c.Actions[0]
	for rel, want := range map[string]bool{
		"inbound/leave.md": true, "inbound/sub/leave.md": false, "inbound/leave.pdf": false, "leave.md": false,
		"notes.txt": true, "x/notes.txt": false, "drafts/a.md": true, "drafts/deep/b.txt": true, "drafts": false,
	} {
		if a.matches(rel) != want {
			t.Errorf("%s: matches %v, want %v", rel, !want, want)
		}
	}
	if a.doFile() != "steps.md" || a.steps() != defaultActionSteps || !a.webEnabled() {
		t.Errorf("doFile %q steps %d web %v", a.doFile(), a.steps(), a.webEnabled())
	}
	for do, file := range map[string]string{"Rewrite it as plan.md": "", "plans/how.md": "plans/how.md", "../x.md": "", "notes.MD": "notes.MD"} {
		if got := (folderAction{Do: do}).doFile(); got != file {
			t.Errorf("doFile(%q) = %q, want %q", do, got, file)
		}
	}
}

// Actions are allowed only in owned spaces, never inherited, and can be locked.
func TestActionsFolderRules(t *testing.T) {
	s, owned, do := folderFilesHarness(t)
	ctx := context.Background()
	config := `{"actions":[{"name":"tidy","on":"inbound/*.md","do":"Tidy it."}]}`
	do(asOwner(ctx, owned), "write", Input{Path: "hr/.metatrash.json", Text: &config}, "")
	child := "{\"inherit\": true}\n"
	do(ctx, "write", Input{Path: "hr/inbound/.metatrash.json", Text: &child}, "")
	_, files, err := owned.snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	settings, err := owned.effectiveSettings(ctx, files, "hr/inbound/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := settings["actions"]; ok {
		t.Error("actions were inherited")
	}
	head, _ := s.repos["public"].head(ctx)
	if _, err := s.Dispatch(ctx, "public", "write", Input{Path: "hr/.metatrash.json", Text: &config, IfInState: head}); err == nil || !strings.Contains(err.Error(), "only in your own spaces") {
		t.Errorf("configured space took actions: %v", err)
	}
	locked := `{"actions":[{"name":"tidy","on":"inbound/*.md","do":"Tidy it."}],"locked":{"actions":"agents"}}`
	do(asOwner(ctx, owned), "write", Input{Path: "hr/.metatrash.json", Text: &locked}, "")
	changed := `{"actions":[],"locked":{"actions":"agents"}}`
	do(ctx, "write", Input{Path: "hr/.metatrash.json", Text: &changed}, "locked_setting")
}

func TestTriggersBetween(t *testing.T) {
	_, owned, do := folderFilesHarness(t)
	ctx := context.Background()
	config := `{"actions":[{"name":"new","on":"inbound/*.md","do":"x"},{"name":"edits","on":"*.md","when":"change","do":"y"}]}`
	do(asOwner(ctx, owned), "write", Input{Path: "hr/.metatrash.json", Text: &config}, "")
	text := "draft\n"
	do(ctx, "write", Input{Path: "hr/policy.md", Text: &text}, "")
	before, _ := owned.head(ctx)
	// A new subfolder brings its own AGENTS.md and .metatrash.json, which never trigger.
	do(ctx, "write", Input{Path: "hr/inbound/leave.md", Text: &text}, "")
	edited := "draft 2\n"
	do(ctx, "write", Input{Path: "hr/policy.md", Text: &edited}, "")
	do(ctx, "write", Input{Path: "hr/runner.md", Text: &text}, "")
	do(ctx, "write", Input{Path: "other/inbound/x.md", Text: &text}, "")
	after, _ := owned.head(ctx)
	triggers, err := owned.triggersBetween(ctx, before, after)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tr := range triggers {
		got = append(got, tr.Folder+" "+tr.Action+" "+tr.File)
	}
	sort.Strings(got)
	want := "hr/ edits hr/policy.md|hr/ new hr/inbound/leave.md"
	if strings.Join(got, "|") != want {
		t.Errorf("triggers %q, want %q", strings.Join(got, "|"), want)
	}
	// A move into the folder is an arrival.
	mid, _ := owned.head(ctx)
	do(ctx, "move", Input{From: "other/inbound/x.md", To: "hr/inbound/x.md"}, "")
	end, _ := owned.head(ctx)
	triggers, err = owned.triggersBetween(ctx, mid, end)
	if err != nil || len(triggers) != 1 || triggers[0].File != "hr/inbound/x.md" || triggers[0].Action != "new" {
		t.Errorf("move: %v %v", triggers, err)
	}
}

// asOwner is a context writing as the space owner through Metatrash Sync,
// the only writer that may change actions.
func asOwner(ctx context.Context, r *repository) context.Context {
	if r.owner == "" {
		r.owner = strings.Repeat("e", 32)
	}
	ctx = context.WithValue(ctx, personWriteKey{}, true)
	return context.WithValue(ctx, mcpIdentityKey{}, mcpIdentity{oauth: &oauthIdentity{UserID: r.owner}})
}

// Only the owner, as a person, can add, change or remove actions.
func TestActionsOwnerOnly(t *testing.T) {
	_, owned, do := folderFilesHarness(t)
	ctx := context.Background()
	owner := asOwner(ctx, owned)
	config := `{"actions":[{"name":"tidy","on":"inbound/*.md","do":"Tidy it."}]}`
	member := context.WithValue(context.WithValue(ctx, personWriteKey{}, true), mcpIdentityKey{}, mcpIdentity{oauth: &oauthIdentity{UserID: strings.Repeat("f", 32)}})
	ownersAgent := context.WithValue(ctx, mcpIdentityKey{}, mcpIdentity{oauth: &oauthIdentity{UserID: owned.owner}})
	for _, c := range []context.Context{ctx, member, ownersAgent} {
		do(c, "write", Input{Path: "hr/.metatrash.json", Text: &config}, "forbidden")
	}
	do(owner, "write", Input{Path: "hr/.metatrash.json", Text: &config}, "")
	// Others may change the rest of the file, but not the actions.
	purpose := `{"purpose":"HR","actions":[{"name":"tidy","on":"inbound/*.md","do":"Tidy it."}]}`
	do(member, "write", Input{Path: "hr/.metatrash.json", Text: &purpose}, "")
	for _, text := range []string{`{"purpose":"HR"}`, `{"purpose":"HR","actions":[{"name":"tidy","on":"*.md","do":"Tidy it."}]}`} {
		do(member, "write", Input{Path: "hr/.metatrash.json", Text: &text}, "forbidden")
	}
	do(ownersAgent, "edit", Input{Path: "hr/.metatrash.json", Edits: []Edit{{OldText: `"inbound/*.md"`, NewText: `"*.md"`}}}, "forbidden")
	empty := `{"purpose":"HR"}`
	do(owner, "write", Input{Path: "hr/.metatrash.json", Text: &empty}, "")
}
