// Package htmlreport renders a project report into a standalone HTML document.
//
// The report body is authored as markdown; this package converts it to HTML,
// wraps it in a self-contained document, and can optionally inline every
// locally served image as a base64 data URI so the result is fully portable.
package htmlreport

import (
	"bytes"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

// Options controls a single render.
type Options struct {
	// Title is the report title, used as the document <title>.
	Title string
	// Markdown is the report body.
	Markdown string
	// InlineImages embeds local assets as base64 data URIs when true;
	// when false the original image URLs are kept as-is.
	InlineImages bool
	// BaseDir is storage.base_dir, used to locate local assets when
	// InlineImages is true.
	BaseDir string
}

// Renderer converts report markdown into a standalone HTML document.
// It is safe for concurrent use.
type Renderer struct {
	markdown goldmark.Markdown
}

// New returns a Renderer backed by a GFM-flavoured markdown parser.
func New() *Renderer {
	return &Renderer{
		markdown: goldmark.New(
			goldmark.WithExtensions(extension.GFM),
			goldmark.WithParserOptions(parser.WithAutoHeadingID()),
			goldmark.WithRendererOptions(html.WithUnsafe()),
		),
	}
}

// Render converts markdown to a full HTML document according to opts.
func (r *Renderer) Render(opts Options) ([]byte, error) {
	var body bytes.Buffer
	if err := r.markdown.Convert([]byte(opts.Markdown), &body); err != nil {
		return nil, err
	}

	document, err := renderDocument(opts.Title, body.String())
	if err != nil {
		return nil, err
	}

	if opts.InlineImages {
		document = inlineImages(document, opts.BaseDir)
	}
	return document, nil
}
