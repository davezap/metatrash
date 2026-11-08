package filenames

import "testing"

func TestNewName(t *testing.T) {
	for _, name := range []string{"report.md", "notes/\u00E9t\u00E9.md", "\u65E5\u672C\u8A9E/\u30E1\u30E2.md", "family \U0001F468\u200D\U0001F469\u200D\U0001F467.md", "\u0645\u06CC\u200C\u062E\u0648\u0627\u0647\u0645.md", "flag \U0001F3F4\U000E0067\U000E0062\U000E0065\U000E006E\U000E0067\U000E007F.md"} {
		if reason := NewName(name); reason != "" {
			t.Errorf("%q refused: %s", name, reason)
		}
	}
	for name, want := range map[string]string{
		"invoice\u202Efdp.exe":  "U+202E",
		"a\u200Bb.md":           "U+200B",
		"\xEF\xBB\xBFreadme.md": "U+FEFF",
		"x/\u2066y\u2069.md":    "U+2066",
		"soft\u00ADhyphen.md":   "U+00AD",
		"mark\u200F.md":         "U+200F",
	} {
		reason := NewName(name)
		if reason == "" || reason[len(reason)-7:len(reason)-1] != want {
			t.Errorf("%q: %q, want %s", name, reason, want)
		}
		if !Path(name) {
			t.Errorf("%q: existing files must stay valid paths", name)
		}
	}
}
