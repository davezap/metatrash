package metatrash

import _ "embed"

const Version = "0.29.0"

//go:embed api/tool-schema.json
var ToolSchema []byte

//go:embed docs/space-README.template.md
var SpaceREADME []byte
