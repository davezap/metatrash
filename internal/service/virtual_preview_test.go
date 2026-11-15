package service

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http/httptest"
	"testing"
)

// previewPicture is a w×h picture: left half red, right half blue, see-through
// at the top left when alpha.
func previewPicture(w, h int, alpha bool) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBA{0, 0, 255, 255}
			if x < w/2 {
				c = color.NRGBA{255, 0, 0, 255}
			}
			if alpha && x < w/4 && y < h/4 {
				c.A = 0
			}
			m.SetNRGBA(x, y, c)
		}
	}
	return m
}

// withOrientation puts an Exif segment holding orientation o after a JPEG's SOI.
func withOrientation(b []byte, o int) []byte {
	out := append([]byte{}, b[:2]...)
	out = append(out, orientationSegment(o)...)
	return append(out, b[2:]...)
}

func TestMakePreview(t *testing.T) {
	var big bytes.Buffer
	jpeg.Encode(&big, previewPicture(3000, 2000, false), &jpeg.Options{Quality: 90})
	body, media, ok := makePreview(bytes.NewReader(big.Bytes()), int64(big.Len()))
	if !ok || media != "image/jpeg" {
		t.Fatalf("large JPEG: ok=%v %s", ok, media)
	}
	m, format, err := image.Decode(bytes.NewReader(body))
	if err != nil || format != "jpeg" || m.Bounds().Dx() != 1280 || m.Bounds().Dy() != 853 {
		t.Fatalf("preview %s %v %v", format, m.Bounds(), err)
	}
	if r, _, b, _ := m.At(100, 400).RGBA(); r>>8 < 200 || b>>8 > 60 {
		t.Errorf("left side not red: %d %d", r>>8, b>>8)
	}
	if bytes.Contains(body, []byte("Exif")) {
		t.Error("preview kept Exif")
	}

	// A phone photo stored on its side shows upright: 6 turns it a quarter clockwise.
	turned := withOrientation(big.Bytes(), 6)
	body, _, ok = makePreview(bytes.NewReader(turned), int64(len(turned)))
	m, _, _ = image.Decode(bytes.NewReader(body))
	if !ok || m.Bounds().Dx() != 853 || m.Bounds().Dy() != 1280 {
		t.Fatalf("orientation 6: %v", m.Bounds())
	}
	// The red left half is now the top.
	if r, _, b, _ := m.At(400, 100).RGBA(); r>>8 < 200 || b>>8 > 60 {
		t.Errorf("top not red after turning: %d %d", r>>8, b>>8)
	}

	// See-through parts keep a PNG.
	var clear bytes.Buffer
	png.Encode(&clear, previewPicture(2000, 1000, true))
	body, media, ok = makePreview(bytes.NewReader(clear.Bytes()), int64(clear.Len()))
	m, format, _ = image.Decode(bytes.NewReader(body))
	if !ok || media != "image/png" || format != "png" || m.Bounds().Dx() != 1280 || m.Bounds().Dy() != 640 {
		t.Fatalf("PNG with alpha: ok=%v %s %v", ok, media, m.Bounds())
	}
	if _, _, _, a := m.At(10, 10).RGBA(); a != 0 {
		t.Errorf("see-through corner has alpha %d", a)
	}

	// Small pictures need none; broken ones get none.
	var small bytes.Buffer
	png.Encode(&small, previewPicture(800, 600, false))
	if _, _, ok := makePreview(bytes.NewReader(small.Bytes()), int64(small.Len())); ok {
		t.Error("small picture got a preview")
	}
	if _, _, ok := makePreview(bytes.NewReader([]byte("not a picture")), 13); ok {
		t.Error("text got a preview")
	}
	cut := big.Bytes()[:big.Len()/2]
	if _, _, ok := makePreview(bytes.NewReader(cut), int64(len(cut))); ok {
		t.Error("cut-off JPEG got a preview")
	}
}

func TestOrient(t *testing.T) {
	// A 2×1 picture: A at (0,0), B at (1,0).
	a, b := color.RGBA{1, 0, 0, 255}, color.RGBA{2, 0, 0, 255}
	m := image.NewRGBA(image.Rect(0, 0, 2, 1))
	m.SetRGBA(0, 0, a)
	m.SetRGBA(1, 0, b)
	for o, want := range map[int][]color.RGBA{
		1: {a, b}, 2: {b, a}, 3: {b, a}, 4: {a, b},
		5: {a, b}, 6: {a, b}, 7: {b, a}, 8: {b, a},
	} {
		out := orient(m, o)
		var got []color.RGBA
		bounds := out.Bounds()
		for y := 0; y < bounds.Dy(); y++ {
			for x := 0; x < bounds.Dx(); x++ {
				got = append(got, out.RGBAAt(x, y))
			}
		}
		if (o >= 5) != (bounds.Dx() == 1) || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("orientation %d: %v %v", o, bounds, got)
		}
	}
}

func TestPreviewPath(t *testing.T) {
	if p := previewPath("assets/Site photo.JPG", "jpg"); p != "assets/Site photo.preview.jpg" {
		t.Error(p)
	}
	if !isPreviewPath("a/b.Preview.jpg") || isPreviewPath("preview/b.jpg") {
		t.Error("isPreviewPath")
	}
	if o := jpegOrientation(bytes.NewReader(withOrientation([]byte{0xFF, 0xD8, 0xFF, 0xD9}, 8))); o != 8 {
		t.Errorf("orientation %d", o)
	}
}

func TestPicturePreviewAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	settings := testAgentSettings(t)
	objects := &fileObjects{memoryObjects: &memoryObjects{objects: map[string][]byte{}}, hashes: map[string]string{}}
	settings.downloads = objects
	runner, err := newAgentRunner(w.s, settings, &memoryJobStore{jobs: map[string]agentJob{}})
	if err != nil {
		t.Fatal(err)
	}
	w.s.agents.Store(runner)
	t.Cleanup(func() { w.s.agents.Store(nil) })
	token := w.connect(w.owner, map[string]string{w.ownedID: "read_write"}, "https://metatrash.com/api/v1/account")
	mcp := w.connect(w.owner, map[string]string{w.ownedID: "read_write"}, "")
	put := func(path, kind string, body []byte) *httptest.ResponseRecorder {
		state := w.tool(mcp, "list", map[string]any{"space": w.ownedName}, "")["state"].(string)
		r := httptest.NewRequest("PUT", "https://metatrash.com/api/v1/account/spaces/"+w.ownedName+"/content?path="+path+"&ifInState="+state, bytes.NewReader(body))
		r.RemoteAddr = "192.0.2.12:1"
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", kind)
		res := httptest.NewRecorder()
		w.h.ServeHTTP(res, r)
		return res
	}
	var big bytes.Buffer
	jpeg.Encode(&big, previewPicture(2400, 1600, false), &jpeg.Options{Quality: 90})
	res := put("photos/site.jpg", "image/jpeg", big.Bytes())
	if res.Code != 201 || !bytes.Contains(res.Body.Bytes(), []byte(`"preview":"photos/site.preview.jpg"`)) {
		t.Fatalf("upload: %d %s", res.Code, res.Body)
	}
	read := w.tool(mcp, "read", map[string]any{"space": w.ownedName, "path": "photos/site.preview.jpg"}, "")
	v := read["file"].(map[string]any)["virtual"].(map[string]any)
	if v["type"] != "image/jpeg" || v["size"].(float64) >= float64(big.Len()) {
		t.Fatalf("preview pointer: %v", v)
	}
	// A preview isn't made from a preview, nor from a small picture.
	var small bytes.Buffer
	png.Encode(&small, previewPicture(300, 200, false))
	if res := put("photos/icon.png", "image/png", small.Bytes()); res.Code != 201 || bytes.Contains(res.Body.Bytes(), []byte(`"preview"`)) {
		t.Fatalf("small upload: %d %s", res.Code, res.Body)
	}
	if res := put("photos/other.preview.jpg", "image/jpeg", big.Bytes()); res.Code != 201 || bytes.Contains(res.Body.Bytes(), []byte(`"preview"`)) {
		t.Fatalf("preview upload: %d %s", res.Code, res.Body)
	}
	// A note someone wrote at the preview's name is left alone.
	w.tool(mcp, "write", map[string]any{"space": w.ownedName, "path": "photos/b.preview.jpg", "text": "mine\n",
		"ifInState": w.tool(mcp, "list", map[string]any{"space": w.ownedName}, "")["state"]}, "")
	if res := put("photos/b.jpg", "image/jpeg", big.Bytes()); res.Code != 201 || bytes.Contains(res.Body.Bytes(), []byte(`"preview"`)) {
		t.Fatalf("upload beside a note: %d %s", res.Code, res.Body)
	}
	if text := w.tool(mcp, "read", map[string]any{"space": w.ownedName, "path": "photos/b.preview.jpg"}, "")["file"].(map[string]any)["text"]; text != "mine\n" {
		t.Fatalf("note replaced: %v", text)
	}
}
