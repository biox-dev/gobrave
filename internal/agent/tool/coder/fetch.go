package coder

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/PuerkitoBio/goquery"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

type FetchParams struct {
	URL     string `json:"url"`
	Format  string `json:"format"`
	Timeout int    `json:"timeout,omitempty"`
}

type fetchTool struct {
	client *http.Client
}

const (
	FetchToolName        = "fetch"
	fetchToolDescription = `Fetches content from a URL and returns it in the specified format.

WHEN TO USE THIS TOOL:
- Use when you need to download content from a URL
- Helpful for retrieving documentation, API responses, or web content
- Useful for getting external information to assist with tasks

HOW TO USE:
- Provide the URL to fetch content from
- Specify the desired output format (text, markdown, or html)
- Optionally set a timeout for the request

FEATURES:
- Supports three output formats: text, markdown, and html
- Automatically handles HTTP redirects
- Sets reasonable timeouts to prevent hanging
- Validates input parameters before making requests

LIMITATIONS:
- Maximum response size is 5MB
- Only supports HTTP and HTTPS protocols
- Cannot handle authentication or cookies
- Some websites may block automated requests

TIPS:
- Use text format for plain text content or simple API responses
- Use markdown format for content that should be rendered with formatting
- Use html format when you need the raw HTML structure
- Set appropriate timeouts for potentially slow websites`
)

// NewFetch 返回 fetch 工具。
func NewFetch() tool.Tool {
	return &fetchTool{client: &http.Client{Timeout: 30 * time.Second}}
}

// Definition 实现 tool.Tool。
func (t *fetchTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        FetchToolName,
		Description: fetchToolDescription,
		InputSchema: tool.Schema("fetch 工具入参", map[string]any{
			"url": tool.StringProperty("The URL to fetch content from"),
			"format": map[string]any{
				"type":        "string",
				"description": "The format to return the content in (text, markdown, or html)",
				"enum":        []string{"text", "markdown", "html"},
			},
			"timeout": tool.NumberProperty("Optional timeout in seconds (max 120)"),
		}, "url", "format"),
		Permission: &tool.Permission{Action: "network"},
	}
}

// Execute 实现 tool.Tool。
func (t *fetchTool) Execute(ctx context.Context, args json.RawMessage) (any, error) {
	var params FetchParams
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, toolError("Failed to parse fetch parameters: " + err.Error())
	}
	if params.URL == "" {
		return nil, toolError("URL parameter is required")
	}

	format := strings.ToLower(params.Format)
	if format != "text" && format != "markdown" && format != "html" {
		return nil, toolError("Format must be one of: text, markdown, html")
	}
	if !strings.HasPrefix(params.URL, "http://") && !strings.HasPrefix(params.URL, "https://") {
		return nil, toolError("URL must start with http:// or https://")
	}

	client := t.client
	if params.Timeout > 0 {
		if params.Timeout > 120 {
			params.Timeout = 120
		}
		client = &http.Client{Timeout: time.Duration(params.Timeout) * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, "GET", params.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "gobrave/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, toolError(fmt.Sprintf("Request failed with status code: %d", resp.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if err != nil {
		return nil, toolError("Failed to read response body: " + err.Error())
	}

	content := string(body)
	contentType := resp.Header.Get("Content-Type")

	switch format {
	case "text":
		if strings.Contains(contentType, "text/html") {
			text, err := extractTextFromHTML(content)
			if err != nil {
				return nil, toolError("Failed to extract text from HTML: " + err.Error())
			}
			return text, nil
		}
		return content, nil
	case "markdown":
		if strings.Contains(contentType, "text/html") {
			markdown, err := convertHTMLToMarkdown(content)
			if err != nil {
				return nil, toolError("Failed to convert HTML to Markdown: " + err.Error())
			}
			return markdown, nil
		}
		return "```\n" + content + "\n```", nil
	default:
		return content, nil
	}
}

func extractTextFromHTML(html string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", err
	}

	text := doc.Text()
	text = strings.Join(strings.Fields(text), " ")

	return text, nil
}

func convertHTMLToMarkdown(html string) (string, error) {
	converter := md.NewConverter("", true, nil)

	markdown, err := converter.ConvertString(html)
	if err != nil {
		return "", err
	}

	return markdown, nil
}
