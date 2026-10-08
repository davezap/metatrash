package metatrash

import _ "embed"

const Version = "0.28.1"

//go:embed api/tool-schema.json
var ToolSchema []byte

//go:embed docs/space-README.template.md
var SpaceREADME []byte
