package static

import "embed"

//go:embed *.js *.css *.svg games
var Files embed.FS
