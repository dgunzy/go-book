// Package publicassets contains the public site's templates and browser assets.
package publicassets

import "embed"

// Files is the immutable asset bundle served by the public web handler.
//
//go:embed templates/*.gohtml static/* players/*
var Files embed.FS
