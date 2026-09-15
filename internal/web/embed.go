package web

import "embed"

// Dist is the built web GUI (ui/, npm run build).
//
//go:embed all:dist
var Dist embed.FS
