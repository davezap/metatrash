package metatrash

import _ "embed"

const Version = "0.60.2"

//go:embed api/tool-schema.json
var ToolSchema []byte

//go:embed docs/space-AGENTS.template.md
var SpaceAGENTS []byte
