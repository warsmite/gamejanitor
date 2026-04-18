package embedded

import "embed"

//go:embed crun-* pasta-* userns-*
var Binaries embed.FS
