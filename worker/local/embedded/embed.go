package embedded

import "embed"

//go:embed bwrap-*
var Binaries embed.FS
