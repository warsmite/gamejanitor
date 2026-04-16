package embedded

import "embed"

//go:embed crun-* pasta-*
var Binaries embed.FS
