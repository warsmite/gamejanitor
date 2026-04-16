package embedded

import "embed"

//go:embed bwrap-* slirp4netns-*
var Binaries embed.FS
