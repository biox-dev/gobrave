package htmlreport

import (
	"bytes"
	"html/template"
)

// documentData is the payload injected into the standalone HTML template.
type documentData struct {
	Title string
	// Body is the already-rendered HTML fragment produced from markdown.
	Body template.HTML
}

// documentTemplate wraps a rendered markdown body into a self-contained
// HTML document with print-friendly, CJK-aware styling.
var documentTemplate = template.Must(template.New("report").Parse(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>{{ .Title }}</title>
<style>
  :root { color-scheme: light; }
  * { box-sizing: border-box; }
  body {
    margin: 0;
    padding: 32px 24px 64px;
    background: #f5f6f8;
    color: #1f2328;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC",
      "Hiragino Sans GB", "Microsoft YaHei", "Source Han Sans SC", sans-serif;
    line-height: 1.7;
    font-size: 15px;
  }
  .report {
    max-width: 900px;
    margin: 0 auto;
    background: #ffffff;
    padding: 40px 48px 56px;
    border-radius: 8px;
    box-shadow: 0 1px 3px rgba(0, 0, 0, 0.08);
  }
  h1, h2, h3, h4, h5, h6 { line-height: 1.3; margin: 1.4em 0 0.6em; }
  h1 { font-size: 1.7em; border-bottom: 1px solid #e4e7eb; padding-bottom: 0.3em; }
  h2 { font-size: 1.4em; }
  h3 { font-size: 1.2em; }
  p { margin: 0.8em 0; }
  a { color: #1677ff; }
  img { max-width: 100%; height: auto; display: block; margin: 1em auto; }
  table {
    width: 100%;
    border-collapse: collapse;
    margin: 1em 0;
    font-size: 0.95em;
  }
  th, td { border: 1px solid #dcdfe4; padding: 8px 12px; text-align: left; }
  th { background: #f2f4f7; font-weight: 600; }
  tr:nth-child(even) td { background: #fafbfc; }
  blockquote {
    margin: 1em 0;
    padding: 0.4em 1em;
    color: #5b6470;
    border-left: 4px solid #d0d7de;
    background: #f6f8fa;
  }
  pre {
    background: #f6f8fa;
    padding: 14px 16px;
    border-radius: 6px;
    overflow: auto;
    font-size: 0.9em;
  }
  code {
    font-family: "SFMono-Regular", Consolas, "Liberation Mono", Menlo, monospace;
    background: rgba(27, 31, 35, 0.06);
    padding: 0.15em 0.4em;
    border-radius: 4px;
    font-size: 0.9em;
  }
  pre code { background: none; padding: 0; }
  hr { border: none; border-top: 1px solid #e4e7eb; margin: 2em 0; }
  @media print {
    body { background: #ffffff; padding: 0; }
    .report { box-shadow: none; border-radius: 0; max-width: none; padding: 0; }
    h1, h2, h3 { break-after: avoid; }
    img, table, pre, blockquote { break-inside: avoid; }
  }
</style>
</head>
<body>
<article class="report">
{{ .Body }}
</article>
</body>
</html>
`))

// renderDocument injects the rendered body into the HTML document template.
func renderDocument(title, body string) ([]byte, error) {
	if title == "" {
		title = "Report"
	}

	var buf bytes.Buffer
	if err := documentTemplate.Execute(&buf, documentData{
		Title: title,
		Body:  template.HTML(body), //nolint:gosec // body is produced by our own markdown renderer
	}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
