//go:build !embed_frontend

package gobrave

import "io/fs"

// EmbeddedFrontendFS returns no assets when built without -tags embed_frontend.
func EmbeddedFrontendFS() (fs.FS, bool) {
	return nil, false
}
