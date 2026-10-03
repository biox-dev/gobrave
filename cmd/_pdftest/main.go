package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	pdf "github.com/stephenafamo/goldmark-pdf"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

func try(name, path string) {
	fontBytes, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("%s: read err %v\n", name, err)
		return
	}
	fpdf := pdf.NewFpdf(context.Background(), pdf.FpdfConfig{Title: name, Orientation: "P", PaperSize: "A4"}, nil)
	for _, st := range []string{"", "B", "I", "BI"} {
		if err := fpdf.AddFont("cjk", st, fontBytes); err != nil {
			fmt.Printf("%s: AddFont(%q) err %v\n", name, st, err)
			return
		}
	}
	tf := pdf.Font{CanUseForText: true, Category: "sans-serif", Family: "cjk", Type: pdf.FontTypeCustom}
	r := pdf.New(
		pdf.OptionFunc(func(c *pdf.Config) { c.PDF = fpdf }),
		pdf.WithBodyFont(tf), pdf.WithHeadingFont(tf), pdf.WithCodeFont(tf),
	)
	md := goldmark.New(goldmark.WithExtensions(extension.GFM), goldmark.WithRenderer(r))
	out := filepath.Join("/tmp", name+".pdf")
	f, err := os.Create(out)
	if err != nil {
		fmt.Printf("%s: create err %v\n", name, err)
		return
	}
	defer f.Close()
	src := "# ABC 中文标题\n\n" +
		"English text 123 与中文混排。这里是一段较长的中文段落，用于观察字体渲染质量。" +
		"Reports mix Latin words and 中文 together.\n\n" +
		"- 列表项 alpha\n- 列表项 中文\n\n" +
		"| 名称 | 数值 |\n|---|---|\n| 样本A | 12 |\n\n" +
		"```go\nfunc main() { println(\"hi\") }\n```\n"
	if err := md.Convert([]byte(src), f); err != nil {
		fmt.Printf("%s: convert err %v\n", name, err)
		return
	}
	fmt.Printf("%s: OK -> %s\n", name, out)
}

func main() {
	try("lxgw", "/tmp/lxgw.ttf")
	try("unifont", "/usr/share/fonts/truetype/unifont/unifont.ttf")
}
