package embedded

import "embed"

//go:embed crun-*
var Binaries embed.FS
