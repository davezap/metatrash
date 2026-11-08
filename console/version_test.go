package console

import (
	"bytes"
	"strings"
	"testing"
	"testing/fstest"

	"metatrash.com/metatrash"
)

func TestVersionListsComponentsAndLibraries(t *testing.T) {
	defer func(c []Component) { extraComponents = c; frontendFS = nil }(extraComponents)
	var out bytes.Buffer
	if err := runVersion([]string{"-core"}, &out); err != nil || out.String() != metatrash.Version+"\n" {
		t.Fatalf("version -core = %q, %v", out.String(), err)
	}
	AddComponents(Component{Name: "Metatrash Company", Version: "9.9.9"})
	SetFrontend(fstest.MapFS{"frontends.json": {Data: []byte(`{"viewer":{"version":"1.2.3","commit":"0123456789abcdef"},"slate":{"version":"4.5.6","commit":"abc","uncommitted":true},"libraries":[{"name":"svelte","version":"5.1.0"},{"name":"@codemirror/view","version":"6.0.0"}]}`)}})
	out.Reset()
	if err := runVersion(nil, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"Metatrash Core", metatrash.Version, "Metatrash Company", "9.9.9", "1.2.3 (0123456)", "4.5.6 (abc, with uncommitted changes)", "Go ", "Front-end libraries", "@codemirror/view", "svelte"} {
		if !strings.Contains(text, want) {
			t.Errorf("version output lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Go libraries") {
		t.Errorf("version without -all lists Go libraries:\n%s", text)
	}
	out.Reset()
	if err := runVersion([]string{"--all"}, &out); err != nil || !strings.Contains(out.String(), "Front-end libraries") {
		t.Fatalf("version --all = %q, %v", out.String(), err)
	}
	if strings.Index(text, "@codemirror/view") > strings.Index(text, "svelte") {
		t.Errorf("libraries are not sorted:\n%s", text)
	}
	if err := runVersion([]string{"extra"}, &out); err == nil {
		t.Error("version accepted an argument")
	}
}
