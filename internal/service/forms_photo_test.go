package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const photoTemplate = `---
form: site-visit
title: Site visit
submitTo: dave/reports/visits/
---

Site: {{text name=site required}}

Damaged? {{yesno name=damaged required}}

Photos of the damage: {{photo name=damage label="Damage" max=3 required show-if="damaged=Yes"}}

Overview: {{photo name=overview}}
`

func testPicture(t *testing.T, kind string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 30))
	for x := 0; x < 40; x++ {
		img.Set(x, x%30, color.RGBA{200, 10, 10, 255})
	}
	var b bytes.Buffer
	var err error
	if kind == "png" {
		err = png.Encode(&b, img)
	} else {
		err = jpeg.Encode(&b, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// withExif puts an Exif segment holding an orientation and a GPS-like
// secret, and a comment, right after a JPEG's start.
func withExif(jpg []byte, orientation int) []byte {
	tiff := []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 0x01, 3, 0, 1, 0, 0, 0, byte(orientation), 0, 0, 0, 0, 0, 0, 0}
	payload := append(append([]byte("Exif\x00\x00"), tiff...), []byte("GPS-SECRET-LOCATION")...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	seg = append(seg, payload...)
	comment := append([]byte{0xFF, 0xFE, 0, 15}, []byte("camera-serial")...)
	out := append([]byte{}, jpg[:2]...)
	out = append(out, seg...)
	out = append(out, comment...)
	return append(out, jpg[2:]...)
}

// withText adds a tEXt chunk after a PNG's IHDR.
func withText(p []byte) []byte {
	data := []byte("Comment\x00GPS-SECRET-LOCATION")
	chunk := make([]byte, 8, 12+len(data))
	binary.BigEndian.PutUint32(chunk, uint32(len(data)))
	copy(chunk[4:], "tEXt")
	chunk = append(chunk, data...)
	crc := make([]byte, 4)
	binary.BigEndian.PutUint32(crc, crc32.ChecksumIEEE(chunk[4:]))
	chunk = append(chunk, crc...)
	at := 8 + 25 // signature, IHDR
	out := append([]byte{}, p[:at]...)
	out = append(out, chunk...)
	return append(out, p[at:]...)
}

func dataURL(media string, b []byte) string {
	return "data:" + media + ";base64," + base64.StdEncoding.EncodeToString(b)
}

func TestPhotoFieldParse(t *testing.T) {
	tpl, err := parseFormTemplate(photoTemplate)
	if err != nil {
		t.Fatal(err)
	}
	if f := tpl.Fields[2]; f.Type != "photo" || photoLimit(f) != 3 || !f.Required || f.ShowIf != "damaged=Yes" {
		t.Fatalf("damage: %+v", f)
	}
	for _, c := range []struct{ body, want string }{
		{"{{photo name=p max=11}}", "max must be a whole number from 1 to 10"},
		{"{{photo name=p default=x}}", "can't have a default"},
		{"{{photo name=p}}\n\n{{text name=q show-if=p}}", "show-if can't depend on a photo"},
		{"{{photo name=p rows=3}}", "does not take rows"},
		{"{{photo name=a max=10}} {{photo name=b max=10}} {{photo name=c}}", "at most 20 photos"},
	} {
		_, err := parseFormTemplate("---\nform: f\nsubmitTo: dave/r/\n---\n" + c.body + "\n")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.body, err)
		}
	}
	if _, err := parseFormTemplate("---\nform: q\nsubmitTo: dave/r/\nquiz: true\n---\n{{photo name=p}}\n"); err == nil || !strings.Contains(err.Error(), "a quiz can't ask for photos") {
		t.Errorf("quiz: %v", err)
	}
}

func TestCleanPhoto(t *testing.T) {
	jpg := withExif(testPicture(t, "jpeg"), 6)
	media, clean, message := cleanPhoto(jpg)
	if message != "" || media != "image/jpeg" {
		t.Fatal(message)
	}
	if bytes.Contains(clean, []byte("GPS-SECRET")) || bytes.Contains(clean, []byte("camera-serial")) {
		t.Fatal("metadata kept")
	}
	if !bytes.Contains(clean, orientationSegment(6)) {
		t.Fatal("orientation lost")
	}
	if o := exifOrientation(orientationSegment(6)[4:]); o != 6 {
		t.Fatalf("orientation %d", o)
	}
	if _, err := jpeg.Decode(bytes.NewReader(clean)); err != nil {
		t.Fatal(err)
	}
	// Upright photos get no Exif at all.
	_, clean, _ = cleanPhoto(withExif(testPicture(t, "jpeg"), 1))
	if bytes.Contains(clean, []byte("Exif")) {
		t.Fatal("Exif kept for an upright photo")
	}

	media, clean, message = cleanPhoto(withText(testPicture(t, "png")))
	if message != "" || media != "image/png" {
		t.Fatal(message)
	}
	if bytes.Contains(clean, []byte("GPS-SECRET")) {
		t.Fatal("PNG text kept")
	}
	if _, err := png.Decode(bytes.NewReader(clean)); err != nil {
		t.Fatal(err)
	}

	if _, _, message := cleanPhoto([]byte("GIF89a not a photo")); message == "" {
		t.Fatal("GIF accepted")
	}
}

func TestPhotoAnswers(t *testing.T) {
	tpl, err := parseFormTemplate(photoTemplate)
	if err != nil {
		t.Fatal(err)
	}
	pic := dataURL("image/jpeg", testPicture(t, "jpeg"))
	check := func(answers map[string]any) (formAnswers, []FieldError) {
		return tpl.checkAnswers(answersJSON(t, answers), "2026-11-09")
	}
	// Required once asked.
	if _, problems := check(map[string]any{"site": "A", "damaged": "Yes"}); len(problems) != 1 || problems[0].Name != "damage" || problems[0].Message != "Add a photo." {
		t.Fatalf("required: %v", problems)
	}
	// Not asked: what was sent is ignored.
	answers, problems := check(map[string]any{"site": "A", "damaged": "No", "damage": []string{"nonsense"}})
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	if _, ok := answers["damage"]; ok {
		t.Fatal("damage answered while not asked")
	}
	if list, ok := answers["overview"].([]photoUpload); !ok || len(list) != 0 {
		t.Fatalf("overview: %#v", answers["overview"])
	}
	// Too many, not a list, not a picture.
	for _, c := range []struct {
		value any
		want  string
	}{
		{[]string{pic, pic, pic, pic}, "Add at most 3 photos."},
		{pic, "Send the photos as a list"},
		{[]string{"data:text/plain;base64,aGVsbG8="}, "send a JPEG or PNG picture"},
		{[]string{"notes/readme.md"}, "neither a data URL nor the path of a picture"},
	} {
		if _, problems := check(map[string]any{"site": "A", "damaged": "Yes", "damage": c.value}); len(problems) != 1 || !strings.Contains(problems[0].Message, c.want) {
			t.Errorf("%v: %v", c.want, problems)
		}
	}
	answers, problems = check(map[string]any{"site": "A", "damaged": "Yes", "damage": []string{pic, "site/before.jpg"}, "overview": []string{pic}})
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	list := answers["damage"].([]photoUpload)
	if len(list) != 2 || list[0].media != "image/jpeg" || len(list[0].data) == 0 || list[1].from != "site/before.jpg" {
		t.Fatalf("damage: %+v", list)
	}

	// The copy shows the stored photos.
	answers["damage"] = []string{"assets/abc-damage-1.jpg", "assets/abc-damage-2.jpg"}
	answers["overview"] = []string{}
	text := tpl.render(answers, submission{Template: "dave/s/f.md", Revision: "r", By: "pat", At: time.Date(2026, 11, 9, 1, 0, 0, 0, time.UTC)})
	for _, want := range []string{
		"  damage: [\"assets/abc-damage-1.jpg\",\"assets/abc-damage-2.jpg\"]\n",
		"Photos of the damage:\n![Damage 1](assets/abc-damage-1.jpg) ![Damage 2](assets/abc-damage-2.jpg)\n",
		"Overview: _(no photo)_\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

// A form with photos sent through the REST resource: the photos are filed
// as virtual files beside the copy, without their metadata.
func TestPhotoFormsAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	settings := testAgentSettings(t)
	objects := &memoryObjects{objects: map[string][]byte{}}
	settings.downloads = objects
	runner, err := newAgentRunner(w.s, settings, &memoryJobStore{jobs: map[string]agentJob{}})
	if err != nil {
		t.Fatal(err)
	}
	w.s.agents.Store(runner)
	t.Cleanup(func() { w.s.agents.Store(nil) })
	if _, err := w.db.db.Exec("UPDATE metatrash_users SET max_private_spaces = 3 WHERE user_id = ?", w.member.ID); err != nil {
		t.Fatal(err)
	}
	review, err := w.s.createOwnedSpace(ctx, w.member.ID, "Reports", randomName(t, "reports-"))
	if err != nil {
		t.Fatal(err)
	}
	author := w.syncTokens(w.member, map[string]string{w.joinID: "read_write", review.ID: "read_write"})
	base := "/api/v1/account/spaces/" + w.joinName + "/"
	res := w.rest(author, "GET", base+"files", "")
	var list map[string]any
	_ = json.Unmarshal(res.Body.Bytes(), &list)
	head, _ := list["state"].(string)
	form := strings.Replace(photoTemplate, "dave/reports/", w.member.Username+"/"+review.Slug+"/", 1)
	if res := w.rest(author, "PUT", base+"file?path=forms/visit.md", mustJSON(map[string]any{"text": form, "ifInState": head})); res.Code != 200 && res.Code != 201 {
		t.Fatalf("write: %d %s", res.Code, res.Body)
	}
	res = w.rest(author, "GET", base+"files", "")
	_ = json.Unmarshal(res.Body.Bytes(), &list)
	head, _ = list["state"].(string)
	// A picture already in the space, which an agent can name by path.
	up := httptest.NewRequest("PUT", "https://metatrash.com"+base+"content?path=site/before.jpg&ifInState="+head, bytes.NewReader(withExif(testPicture(t, "jpeg"), 3)))
	up.RemoteAddr = "192.0.2.11:1"
	up.Header.Set("Authorization", "Bearer "+author)
	up.Header.Set("Content-Type", "image/jpeg")
	upRes := httptest.NewRecorder()
	w.h.ServeHTTP(upRes, up)
	if upRes.Code != 200 && upRes.Code != 201 {
		t.Fatalf("upload: %d %s", upRes.Code, upRes.Body)
	}

	person := w.syncToken(w.owner, w.joinID, "read_only")
	pic := dataURL("image/jpeg", withExif(testPicture(t, "jpeg"), 6))
	res = w.rest(person, "POST", base+"submissions", mustJSON(map[string]any{"path": "forms/visit.md", "answers": map[string]any{"site": "Depot", "damaged": "Yes", "damage": []string{pic, "site/before.jpg"}}}))
	if res.Code != 201 {
		t.Fatalf("submit: %d %s", res.Code, res.Body)
	}
	var receipt struct {
		Target  string         `json:"target"`
		Text    string         `json:"text"`
		Answers map[string]any `json:"answers"`
	}
	_ = json.Unmarshal(res.Body.Bytes(), &receipt)
	paths := photoPaths(receipt.Answers["damage"])
	if len(paths) != 2 || !strings.HasPrefix(paths[0], "assets/") || !strings.HasSuffix(paths[1], "-damage-2.jpg") {
		t.Fatalf("answers: %v", receipt.Answers)
	}
	if !strings.Contains(receipt.Text, "![Damage 1]("+paths[0]+")") {
		t.Fatalf("copy:\n%s", receipt.Text)
	}
	repo := w.s.ownedRepository(review.ID)
	_, files, err := repo.snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		f, ok := files["visits/"+p]
		if !ok || f.Virtual == nil || f.Virtual.Type != "image/jpeg" {
			t.Fatalf("%s: %+v", p, f)
		}
		b, err := w.s.virtualContent(ctx, repo, *f.Virtual)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte("GPS-SECRET")) {
			t.Errorf("%s kept its metadata", p)
		}
	}
	if _, ok := files["visits/assets/AGENTS.md"]; !ok {
		t.Error("assets folder has no AGENTS.md")
	}
	// A path that isn't a picture in the space.
	res = w.rest(person, "POST", base+"submissions", mustJSON(map[string]any{"path": "forms/visit.md", "answers": map[string]any{"site": "Depot", "damaged": "Yes", "damage": []string{"site/missing.jpg"}}}))
	if res.Code != 400 || !strings.Contains(res.Body.String(), "not a picture in this space") {
		t.Fatalf("missing picture: %d %s", res.Code, res.Body)
	}
}
