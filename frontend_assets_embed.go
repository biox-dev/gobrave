//go:build embed_frontend

package gobrave

import (
	"embed"
	"io/fs"
)

// embeddedWebFiles contains the whole frontend directory when built with -tags embed_frontend.
//go:embed web
var embeddedWebFiles embed.FS

// EmbeddedFrontendFS returns embedded frontend assets and whether embedding is enabled.
func EmbeddedFrontendFS() (fs.FS, bool) {
	return embeddedWebFiles, true
}
