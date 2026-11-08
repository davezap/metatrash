package service

import _ "embed"

// frontendsJSON records the Slate and Slate Viewer sources in Core's own site
// build (written by scripts/build-frontends.ps1). The console's version
// command lists them (0.95.13).
//
//go:embed web/frontends.json
var frontendsJSON []byte

// FrontendsJSON returns Core's web/frontends.json.
func FrontendsJSON() []byte { return frontendsJSON }
