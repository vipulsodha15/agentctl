// Package web exposes the SPA build artifacts as an embedded filesystem.
//
// The Vite build under web/dist/ is owned by the M3-C SPA worktree; this
// package only carries the embed directive so internal/websrv can serve
// whatever lands in dist/.
package web

import "embed"

//go:embed all:dist
var FS embed.FS
