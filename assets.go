package metatrash

import _ "embed"

const Version = "0.15.4"

//go:embed api/tool-schema.json
var ToolSchema []byte

//go:embed docs/space-README.template.md
var SpaceREADME []byte
