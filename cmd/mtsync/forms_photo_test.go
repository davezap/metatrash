package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddPhotos(t *testing.T) {
	dir := t.TempDir()
	pic := filepath.Join(dir, "crack.jpg")
	if err := os.WriteFile(pic, []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00rest"), 0o600); err != nil {
		t.Fatal(err)
	}
	txt := filepath.Join(dir, "notes.txt")
	_ = os.WriteFile(txt, []byte("hello"), 0o600)
	var flags photoFlags
	if err := flags.Set("damage"); err == nil {
		t.Fatal("no = accepted")
	}
	if err := flags.Set("damage=" + pic); err != nil {
		t.Fatal(err)
	}
	out, err := addPhotos(json.RawMessage(`{"site":"Depot","damage":["site/before.jpg"]}`), flags)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	list, _ := got["damage"].([]any)
	if got["site"] != "Depot" || len(list) != 2 || list[0] != "site/before.jpg" || !strings.HasPrefix(list[1].(string), "data:image/jpeg;base64,/9j/") {
		t.Fatalf("%s", out)
	}
	if _, err := addPhotos(json.RawMessage(`{}`), photoFlags{"damage=" + txt}); err == nil {
		t.Fatal("text accepted as a photo")
	}
}
