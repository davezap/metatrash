package metatrash

import _ "embed"

const Version = "0.77.1"

//go:embed api/tool-schema.json
var ToolSchema []byte

//go:embed docs/space-AGENTS.template.md
var SpaceAGENTS []byte
