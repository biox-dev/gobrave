package htmlreport

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/internal/utils"
	"golang.org/x/net/html"
)

// dataURLPrefixes are the public URL prefixes whose files live under
// <baseDir>/data (see router.serveStatic).
var dataURLPrefixes = []string{"/data-analysis", "/images-data", "/data-project"}

// inlineImages rewrites every <img src> that points to a locally served asset
// into a base64 data URI, so the whole document becomes self-contained and can
// be viewed or downloaded offline. Remote/unresolvable images are left as-is.
func inlineImages(document []byte, baseDir string) []byte {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return document
	}

	root, err := html.Parse(bytes.NewReader(document))
	if err != nil {
		return document
	}

	changed := false
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "img" {
			for i := range n.Attr {
				if n.Attr[i].Key != "src" {
					continue
				}
				if dataURI, ok := assetDataURI(baseDir, n.Attr[i].Val); ok {
					n.Attr[i].Val = dataURI
					changed = true
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)

	if !changed {
		return document
	}

	var out bytes.Buffer
	if err := html.Render(&out, root); err != nil {
		return document
	}
	return out.Bytes()
}

// assetDataURI resolves a document image URL to a local file and returns it as
// a base64 data URI. It reports false when the URL is not a locally served
// asset or the file cannot be read.
func assetDataURI(baseDir, rawURL string) (string, bool) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || strings.HasPrefix(rawURL, "data:") {
		return "", false
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}
	// Only http(s) or scheme-less (relative) URLs can reference local assets.
	if parsed.Scheme != "" && parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}

	filePath, ok := assetFilePath(baseDir, parsed.Path)
	if !ok {
		return "", false
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", false
	}

	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(filePath)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return fmt.Sprintf("data:%s;base64,%s", contentType, base64.StdEncoding.EncodeToString(data)), true
}

// assetFilePath maps a public asset URL path to its on-disk location under
// baseDir, guarding against path traversal.
func assetFilePath(baseDir, urlPath string) (string, bool) {
	urlPath = strings.TrimSpace(urlPath)
	if urlPath == "" {
		return "", false
	}

	if hasURLPrefix(urlPath, "/images") {
		rel := strings.TrimPrefix(urlPath, "/images")
		return safeJoin(utils.ResolveImageDir(baseDir), rel)
	}

	for _, prefix := range dataURLPrefixes {
		if hasURLPrefix(urlPath, prefix) {
			rel := strings.TrimPrefix(urlPath, prefix)
			return safeJoin(filepath.Join(baseDir, "data"), rel)
		}
	}

	return "", false
}

// safeJoin joins root with a cleaned relative path and validates the result
// stays inside root.
func safeJoin(root, rel string) (string, bool) {
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return "", false
	}

	cleaned := path.Clean("/" + rel) // collapses ".." so traversal cannot escape
	full := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(cleaned, "/")))

	safe, err := utils.SafePathUnderBase(root, full)
	if err != nil {
		return "", false
	}
	return safe, true
}

// hasURLPrefix reports whether p equals prefix or is nested under it.
func hasURLPrefix(p, prefix string) bool {
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}
