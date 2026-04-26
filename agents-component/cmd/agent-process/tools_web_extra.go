// Package main — tools_web_extra.go implements the additional "web" skill domain tools:
//
//   - web_search: vault-delegated AI web search via the Tavily API.
//     The agent never touches the search API key — the Vault executes the call
//     and returns only the ranked result set (ADR-004).
//
//   - web_extract: direct outbound HTTP fetch with HTML-to-text/markdown
//     conversion. No credentials required — public URLs only.
//     HTML parsing uses golang.org/x/net/html (already a transitive dependency).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/cerberOS/agents-component/pkg/types"
	"golang.org/x/net/html"
)

// ── web_search ────────────────────────────────────────────────────────────────

// webSearchTool dispatches a web search to the Vault, which calls the Tavily
// AI Search API using its stored search_api_key and returns only the ranked
// result set. The agent never receives the API key (ADR-004).
//
// TimeoutSeconds = 40: vault deadline = 30s, VaultExecutor local timer = 35s,
// dispatchTool context = 40s. The 5s gap ensures timer.C fires before ctx.Done()
// so the LLM always receives TOOL_TIMEOUT (IsError=true) on timeout, not the
// steering-interrupt TOOL_INTERRUPTED path (IsError=false).
func webSearchTool(ve *VaultExecutor) SkillTool {
	return SkillTool{
		Label:                   "Web Search",
		RequiredCredentialTypes: []string{"search_api_key"},
		TimeoutSeconds:          40,
		Definition: anthropic.ToolParam{
			Name: "web_search",
			Description: anthropic.String(
				"Search the web using AI-powered search and get ranked results with title, URL, and content snippet. " +
					"Do NOT use for fetching a specific known URL — use web_fetch for that. " +
					"Do NOT use for authenticated APIs — those require vault execution."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Plain-language search query. Be specific and descriptive for best results. Do not use boolean operators or special syntax.",
					},
					"max_results": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of results to return (1–20). Defaults to 5. Use a higher value only when breadth matters more than speed.",
					},
					"include_domains": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Restrict results to these domains (e.g. [\"docs.python.org\", \"github.com\"]). Omit to search the open web.",
					},
					"exclude_domains": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Exclude results from these domains. Omit to apply no domain exclusions.",
					},
				},
				Required: []string{"query"},
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			return executeWebSearch(ctx, ve, raw)
		},
	}
}

func executeWebSearch(ctx context.Context, ve *VaultExecutor, raw json.RawMessage) ToolResult {
	var params struct {
		Query          string   `json:"query"`
		MaxResults     int      `json:"max_results,omitempty"`
		IncludeDomains []string `json:"include_domains,omitempty"`
		ExcludeDomains []string `json:"exclude_domains,omitempty"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("invalid parameters: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}
	if params.Query == "" {
		return ToolResult{
			Content: "query parameter is required",
			IsError: true,
		}
	}

	opParams, err := json.Marshal(params)
	if err != nil {
		return ToolResult{
			Content: fmt.Sprintf("failed to encode operation params: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}

	onUpdate := func(p types.VaultOperationProgress) {
		ve.log.Info("vault web_search: progress",
			"request_id", p.RequestID,
			"progress_type", p.ProgressType,
			"message", p.Message,
			"elapsed_ms", p.ElapsedMS,
		)
	}

	// vault TimeoutSeconds = 30; local deadline = 30 + 5 = 35s (matches TimeoutSeconds above).
	return ve.Execute(ctx, "web_search", "search_api_key", opParams, 30, onUpdate)
}

// ── web_extract ───────────────────────────────────────────────────────────────

// webExtractTool fetches a public URL and returns its content as clean text,
// Markdown, or a hyperlink list — stripping HTML boilerplate via the HTML parser.
func webExtractTool() SkillTool {
	return SkillTool{
		Label:                   "Web Content Extraction",
		RequiredCredentialTypes: nil,
		TimeoutSeconds:          60,
		Definition: anthropic.ToolParam{
			Name: "web_extract",
			Description: anthropic.String(
				"Fetch a public URL and return its content as clean text or Markdown, stripping HTML boilerplate. " +
					"Do NOT use for binary files or URLs requiring authentication. " +
					"Do NOT use to search the web — use web_search for that."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"url": map[string]interface{}{
						"type":        "string",
						"description": "The full URL of the page to extract content from (must be http:// or https://). Must be publicly accessible.",
					},
					"format": map[string]interface{}{
						"type":        "string",
						"description": "Output format: 'text' (default) strips HTML and returns plain text; 'markdown' preserves headings and links; 'links' returns only the list of hyperlinks found on the page.",
						"enum":        []string{"text", "markdown", "links"},
					},
				},
				Required: []string{"url"},
			},
		},
		Execute: executeWebExtract,
	}
}

func executeWebExtract(ctx context.Context, raw json.RawMessage) ToolResult {
	var params struct {
		URL    string `json:"url"`
		Format string `json:"format"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("invalid parameters: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}
	if params.Format == "" {
		params.Format = "text"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, params.URL, nil)
	if err != nil {
		return ToolResult{
			Content: fmt.Sprintf("failed to build request: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error(), "url": params.URL},
		}
	}
	req.Header.Set("User-Agent", "aegis-agent/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		content := fmt.Sprintf("HTTP request failed: %v", err)
		if ctx.Err() == context.DeadlineExceeded {
			content = "TOOL_TIMEOUT: web_extract did not complete within the allowed time"
		}
		return ToolResult{
			Content: content,
			IsError: true,
			Details: map[string]interface{}{"error": err.Error(), "url": params.URL},
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxContentBytes))
	if err != nil {
		return ToolResult{
			Content: fmt.Sprintf("failed to read response body: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error(), "url": params.URL},
		}
	}

	if resp.StatusCode >= 400 {
		return ToolResult{
			Content: fmt.Sprintf("HTTP %d fetching %s", resp.StatusCode, params.URL),
			IsError: true,
			Details: map[string]interface{}{"url": params.URL, "status_code": resp.StatusCode},
		}
	}

	var extracted string
	switch params.Format {
	case "links":
		links := extractLinks(body)
		extracted = strings.Join(links, "\n")
		if extracted == "" {
			extracted = "(no links found)"
		}
	case "markdown":
		extracted = htmlToMarkdown(body)
	default: // "text"
		extracted = htmlToText(body)
	}

	if len(extracted) > maxContentBytes {
		extracted = extracted[:maxContentBytes] + "\n[CONTENT TRUNCATED — extracted content exceeded 16KB limit]"
	}

	return ToolResult{
		Content: extracted,
		Details: map[string]interface{}{
			"url":         params.URL,
			"format":      params.Format,
			"status_code": resp.StatusCode,
			"bytes_read":  len(body),
		},
	}
}

// ── HTML extraction helpers ────────────────────────────────────────────────────

// htmlToText extracts visible text from HTML using the tree-based parser,
// skipping non-visible subtrees (script, style, head, noscript) and inserting
// newlines at block-level element boundaries.
func htmlToText(body []byte) string {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		// fallback: return raw bytes with markup present — better than nothing
		return strings.TrimSpace(string(body))
	}
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "head", "noscript", "iframe", "svg", "canvas":
				return // skip subtree entirely
			case "br":
				sb.WriteByte('\n')
				return
			case "p", "div", "section", "article", "header", "footer", "nav",
				"h1", "h2", "h3", "h4", "h5", "h6", "li", "tr", "blockquote":
				sb.WriteByte('\n')
			}
		}
		if n.Type == html.TextNode {
			text := strings.TrimSpace(n.Data)
			if text != "" {
				sb.WriteString(text)
				sb.WriteByte(' ')
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	result := strings.TrimSpace(sb.String())
	for strings.Contains(result, "\n\n\n") {
		result = strings.ReplaceAll(result, "\n\n\n", "\n\n")
	}
	return result
}

// htmlToMarkdown converts key HTML structural elements to Markdown syntax and
// returns the result as a string. Elements without a Markdown equivalent fall
// back to plain text extraction.
func htmlToMarkdown(body []byte) string {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return htmlToText(body)
	}
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "head", "noscript", "iframe", "svg", "canvas":
				return
			case "h1", "h2", "h3", "h4", "h5", "h6":
				level := int(n.Data[1] - '0')
				sb.WriteString("\n" + strings.Repeat("#", level) + " ")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
				sb.WriteString("\n\n")
				return
			case "p", "blockquote":
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
				sb.WriteString("\n\n")
				return
			case "li":
				sb.WriteString("- ")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
				sb.WriteByte('\n')
				return
			case "br":
				sb.WriteByte('\n')
				return
			case "strong", "b":
				sb.WriteString("**")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
				sb.WriteString("**")
				return
			case "em", "i":
				sb.WriteString("_")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
				sb.WriteString("_")
				return
			case "code":
				sb.WriteString("`")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
				sb.WriteString("`")
				return
			case "a":
				href := htmlAttr(n, "href")
				var textBuf strings.Builder
				var collectText func(*html.Node)
				collectText = func(cn *html.Node) {
					if cn.Type == html.TextNode {
						textBuf.WriteString(cn.Data)
					}
					for c := cn.FirstChild; c != nil; c = c.NextSibling {
						collectText(c)
					}
				}
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					collectText(c)
				}
				text := strings.TrimSpace(textBuf.String())
				if href != "" && text != "" {
					sb.WriteString("[" + text + "](" + href + ")")
				} else if text != "" {
					sb.WriteString(text)
				}
				return
			}
		}
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	result := strings.TrimSpace(sb.String())
	for strings.Contains(result, "\n\n\n") {
		result = strings.ReplaceAll(result, "\n\n\n", "\n\n")
	}
	return result
}

// extractLinks returns the href value of every <a> element in the HTML.
func extractLinks(body []byte) []string {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil
	}
	var links []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			if href := htmlAttr(n, "href"); href != "" {
				links = append(links, href)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return links
}

// htmlAttr returns the value of the named attribute on an element node,
// or "" if the attribute is absent.
func htmlAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
