package static

import "embed"

//go:embed *.js *.css *.svg
var Files embed.FS
