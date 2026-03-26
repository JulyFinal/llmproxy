// Package assets embeds the static UI files into the binary.
package assets

import "embed"

//go:embed static/*
var Static embed.FS
