package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

type frontendBundle struct {
	manifest appManifest
	files    map[string][]byte
	editor   map[string][]byte
}

// SetFrontend is a construction-time operation, before Handler or serving.
// It snapshots validated files so two editions in one process stay isolated.
func (s *Service) SetFrontend(source fs.FS) error {
	if s.configurationFrozen || s.frontend != nil {
		return fmt.Errorf("frontend already configured")
	}
	data, err := fs.ReadFile(source, ".vite/manifest.json")
	if err != nil {
		return fmt.Errorf("frontend manifest: %w", err)
	}
	var manifest appManifest
	if err := json.Unmarshal(data, &manifest); err != nil || len(manifest) == 0 {
		return fmt.Errorf("invalid frontend manifest")
	}
	if manifest.entry("src/main.ts") == nil {
		return fmt.Errorf("frontend needs src/main.ts entry")
	}
	files := map[string][]byte{}
	read := func(name string) error {
		if !fs.ValidPath(name) || !strings.HasPrefix(name, "assets/") || strings.ContainsAny(name, "?#\\") {
			return fmt.Errorf("invalid frontend asset path %q", name)
		}
		if _, ok := files[name]; ok {
			return nil
		}
		content, err := fs.ReadFile(source, name)
		if err != nil {
			return fmt.Errorf("frontend asset %s: %w", name, err)
		}
		files[name] = content
		return nil
	}
	for _, chunk := range manifest {
		if err := read(chunk.File); err != nil {
			return err
		}
		for _, css := range chunk.CSS {
			if err := read(css); err != nil {
				return err
			}
		}
		for _, imported := range append(append([]string{}, chunk.Imports...), chunk.DynamicImports...) {
			if _, ok := manifest[imported]; !ok {
				return fmt.Errorf("missing frontend import %q", imported)
			}
		}
		for _, asset := range chunk.Assets {
			if err := read(asset); err != nil {
				return err
			}
		}
	}

	editor := map[string][]byte{}
	for _, name := range []string{"slate.js", "slate.css"} {
		content, err := fs.ReadFile(source, "slate/"+name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("frontend editor %s: %w", name, err)
		}
		if len(content) == 0 {
			return fmt.Errorf("empty frontend editor %s", name)
		}
		editor[name] = append([]byte{}, content...)
	}
	if len(editor) == 1 {
		return fmt.Errorf("frontend editor requires both slate.js and slate.css")
	}
	s.frontend = &frontendBundle{manifest: manifest, files: files, editor: editor}
	return nil
}
func (h *httpAdapter) frontendEntry(entry string) *appBuild {
	if bundle := h.service.frontend; bundle != nil {
		return bundle.manifest.entry(entry)
	}
	return appManifestOnce().entry(entry)
}
func (h *httpAdapter) frontendAsset(name string) ([]byte, error) {
	if bundle := h.service.frontend; bundle != nil {
		data, ok := bundle.files["assets/"+name]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return data, nil
	}
	return appFiles.ReadFile("web/app/assets/" + name)
}

// Editor assets belong to the same instance as its application bundle.
// A baseline bundle may omit them and use the embedded public editor.
func (h *httpAdapter) editorAsset(name string, baseline []byte) []byte {
	if bundle := h.service.frontend; bundle != nil && len(bundle.editor) != 0 {
		return bundle.editor[name]
	}
	return baseline
}
