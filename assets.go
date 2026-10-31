package metatrash

import _ "embed"

const Version = "0.74.0"

//go:embed api/tool-schema.json
var ToolSchema []byte

//go:embed docs/space-AGENTS.template.md
var SpaceAGENTS []byte
