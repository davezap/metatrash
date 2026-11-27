package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
)

func TestNewFilesNeedReading(t *testing.T) {
	w := newOAuthWorld(t)
	publisher := w.syncToken(w.member, w.joinID, "read_write")
	reader := w.syncToken(w.owner, w.joinID, "read_write")
	base := "/api/v1/account/spaces/" + w.joinName + "/"
	call := func(token, method, resource, body string, want int) map[string]any {
		t.Helper()
		res := w.rest(token, method, base+resource, body)
		if res.Code != want {
			t.Fatalf("%s %s %s: %d %s", method, resource, body, res.Code, res.Body)
		}
		var out map[string]any
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		return out
	}
	head := func() string {
		return call(publisher, "GET", "files", "", 200)["state"].(string)
	}
	put := func(path, text string) {
		t.Helper()
		res := w.rest(publisher, "PUT", base+"file?path="+url.QueryEscape(path), mustJSON(map[string]any{"text": text, "ifInState": head()}))
		if res.Code != 200 && res.Code != 201 {
			t.Fatalf("write %s: %d %s", path, res.Code, res.Body)
		}
	}
	unread := func() map[string]bool {
		t.Helper()
		got := call(reader, "GET", "acknowledgements", "", 200)
		out := map[string]bool{}
		for _, f := range got["folders"].([]any) {
			f := f.(map[string]any)
			for _, file := range f["files"].([]any) {
				file := file.(map[string]any)
				if file["outstanding"].(bool) {
					out[file["path"].(string)] = true
				}
			}
		}
		if int(got["outstanding"].(float64)) != len(out) {
			t.Fatalf("outstanding %v, files %v", got["outstanding"], out)
		}
		return out
	}

	put("news/old.md", "Old news\n")
	// Not with publishedVersion.
	res := w.rest(publisher, "PUT", base+"file?path="+url.QueryEscape("news/.metatrash.json"), mustJSON(map[string]any{"text": `{"services":[{"type":"acknowledge","newFiles":true}],"publishedVersion":1}`, "ifInState": head()}))
	if res.Code != 400 {
		t.Fatalf("with publishedVersion: %d %s", res.Code, res.Body)
	}
	put("news/.metatrash.json", `{"services":[{"type":"acknowledge","newFiles":true}]}`)
	// A file already there is not asked for.
	if got := unread(); len(got) != 0 {
		t.Fatalf("on: %v", got)
	}
	put("news/monday.md", "Monday\n")
	put("news/sub/tuesday.md", "Tuesday\n")
	put("news/notes.txt", "not tracked\n")
	got := unread()
	if len(got) != 2 || !got["news/monday.md"] || !got["news/sub/tuesday.md"] {
		t.Fatalf("new files: %v", got)
	}
	folders := call(reader, "GET", "acknowledgements", "", 200)["folders"].([]any)
	if len(folders) != 1 || folders[0].(map[string]any)["newFiles"] != true || folders[0].(map[string]any)["version"].(float64) != 0 {
		t.Fatalf("folder: %v", folders)
	}
	call(reader, "POST", "acknowledgements", `{"path":"news/monday.md","version":1}`, 409)
	call(reader, "POST", "acknowledgements", `{"path":"news/old.md","version":0}`, 404)
	call(reader, "POST", "acknowledgements", `{"path":"news/monday.md","version":0}`, 200)
	if got := unread(); len(got) != 1 || !got["news/sub/tuesday.md"] {
		t.Fatalf("after reading: %v", got)
	}
	// An edit does not ask again.
	put("news/monday.md", "Monday, corrected\n")
	if got := unread(); len(got) != 1 {
		t.Fatalf("after edit: %v", got)
	}
	report := call(publisher, "GET", "acknowledgements?report=true", "", 200)
	if len(report["files"].([]any)) != 2 {
		t.Fatalf("report: %v", report)
	}
	for _, p := range report["people"].([]any) {
		p := p.(map[string]any)
		want := 2.0
		if p["name"] == w.owner.Username {
			want = 1
		}
		if p["outstanding"].(float64) != want {
			t.Fatalf("report person: %v", p)
		}
	}
	// Turning it off asks for nothing; on again, only files added after.
	put("news/.metatrash.json", `{"services":[{"type":"acknowledge"}]}`)
	if got := unread(); len(got) != 0 {
		t.Fatalf("off: %v", got)
	}
	put("news/.metatrash.json", `{"services":[{"type":"acknowledge","newFiles":true}]}`)
	if got := unread(); len(got) != 0 {
		t.Fatalf("on again: %v", got)
	}
	put("news/wednesday.md", "Wednesday\n")
	if got := unread(); len(got) != 1 || !got["news/wednesday.md"] {
		t.Fatalf("on again, new file: %v", got)
	}
	// A subfolder with a published version keeps its own versions.
	put("news/policy/rule.md", "Rule\n")
	put("news/policy/.metatrash.json", `{"services":[{"type":"acknowledge"}],"publishedVersion":1}`)
	if got := unread(); len(got) != 2 || !got["news/policy/rule.md"] || !got["news/wednesday.md"] {
		t.Fatalf("published subfolder: %v", got)
	}
	call(reader, "POST", "acknowledgements", `{"path":"news/policy/rule.md","version":0}`, 409)
	call(reader, "POST", "acknowledgements", `{"path":"news/policy/rule.md","version":1}`, 200)
}

func TestNewFilesSetting(t *testing.T) {
	for text, ok := range map[string]bool{
		`{"services":[{"type":"acknowledge","newFiles":true}]}`:                      true,
		`{"services":[{"type":"acknowledge","newFiles":true}],"publishedVersion":2}`: false,
		`{"services":[{"type":"github","repo":"a/b","newFiles":true}]}`:              false,
		`{"services":[{"type":"email","newFiles":true}]}`:                            false,
	} {
		if _, err := parseFolderConfig(text); (err == nil) != ok {
			t.Errorf("%s: %v", text, err)
		}
	}
}

// The folders and files asked for, without a database (the public space).
func TestNewFilesFolders(t *testing.T) {
	_, r, do := folderFilesHarness(t)
	ctx := context.Background()
	person := context.WithValue(ctx, personWriteKey{}, true)
	write := func(path, text string) {
		t.Helper()
		c := ctx
		if isFolderConfig(path) {
			c = person
		}
		do(c, "write", Input{Path: path, Text: &text}, "")
	}
	asked := func() map[string]string {
		t.Helper()
		st, files, err := r.snapshot(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		folders, err := r.readingFolders(ctx, st, files)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, f := range folders {
			for _, file := range f.Files {
				if file.Tracked {
					out[file.Path] = fmt.Sprintf("%s v%d new=%v", f.Folder, f.Version, f.NewFiles)
				}
			}
		}
		return out
	}
	write("news/old.md", "Old\n")
	write("news/.metatrash.json", `{"services":[{"type":"acknowledge","newFiles":true}]}`)
	if got := asked(); len(got) != 0 {
		t.Fatalf("on: %v", got)
	}
	write("news/monday.md", "Monday\n")
	write("news/sub/tuesday.md", "Tuesday\n")
	write("news/notes.txt", "not tracked\n")
	if got := asked(); len(got) != 2 || got["news/monday.md"] != "news/ v0 new=true" || got["news/sub/tuesday.md"] == "" {
		t.Fatalf("new files: %v", got)
	}
	// An edit keeps the file asked for (reading any text counts).
	write("news/monday.md", "Monday, corrected\n")
	if got := asked(); len(got) != 2 {
		t.Fatalf("after edit: %v", got)
	}
	// Off asks for nothing; on again, only what arrives after.
	write("news/.metatrash.json", `{"services":[{"type":"acknowledge"}]}`)
	if got := asked(); len(got) != 0 {
		t.Fatalf("off: %v", got)
	}
	write("news/.metatrash.json", `{"services":[{"type":"acknowledge","newFiles":true}]}`)
	write("news/wednesday.md", "Wednesday\n")
	if got := asked(); len(got) != 1 || got["news/wednesday.md"] == "" {
		t.Fatalf("on again: %v", got)
	}
	// A subfolder that publishes versions keeps them.
	write("news/policy/rule.md", "Rule\n")
	write("news/policy/.metatrash.json", `{"services":[{"type":"acknowledge"}],"publishedVersion":1}`)
	if got := asked(); len(got) != 2 || got["news/policy/rule.md"] != "news/policy/ v1 new=false" {
		t.Fatalf("published subfolder: %v", got)
	}
	// Moving a file in counts as new; a file already asked for stays so.
	write("elsewhere.md", "Moved in\n")
	do(ctx, "move", Input{From: "elsewhere.md", To: "news/moved.md"}, "")
	if got := asked(); len(got) != 3 || got["news/moved.md"] == "" {
		t.Fatalf("moved in: %v", got)
	}
}

func TestNewFilesStatus(t *testing.T) {
	idx := indexAcks([]ackRow{{UserID: "u", FileID: "f", Blob: "old", Version: 0}})
	f := policyFile{ID: "f", Blob: "new"}
	if ack := idx.statusIn(policyFolder{NewFiles: true}, "u", f); ack == nil || !ack.Current {
		t.Fatalf("new files: %+v", ack)
	}
	if ack := idx.statusIn(policyFolder{}, "u", f); ack == nil || ack.Current {
		t.Fatalf("published: %+v", ack)
	}
	if ack := idx.statusIn(policyFolder{NewFiles: true}, "v", f); ack != nil {
		t.Fatalf("someone else: %+v", ack)
	}
}
