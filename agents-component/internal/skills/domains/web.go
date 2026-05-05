package domains

import "github.com/cerberOS/agents-component/pkg/types"

// WebDomain returns the "web" skill domain, which provides HTTP fetch,
// web search, and content extraction capabilities.
//
// Routing split:
//   - web.fetch and web.extract: direct outbound HTTP via internal/http.
//     No vault execution needed — public URLs only, no credentials injected.
//   - web.search: vault-delegated execution (credential_type: "search_api_key").
//     The Orchestrator routes this to POST /execute on the vault engine, which
//     resolves TAVILY_API_KEY from OpenBao, calls the Tavily AI Search API,
//     and returns only the operation_result (no API key in the response).
func WebDomain() *types.SkillNode {
	return &types.SkillNode{
		Name:  "web",
		Level: "domain",
		Children: map[string]*types.SkillNode{
			"web.fetch": {
				Name:                    "web.fetch",
				Level:                   "command",
				Label:                   "HTTP Fetch",
				Description:             "Fetch the content of a public URL via HTTP GET or POST. Returns body and status code. Do not use for authenticated APIs or search — use web.search for search queries.",
				TimeoutSeconds:          120,
				RequiredCredentialTypes: []string{},
				Spec: &types.SkillSpec{
					Parameters: map[string]types.ParameterDef{
						"url": {
							Type:        "string",
							Required:    true,
							Description: "The full URL to fetch (must be http:// or https://). Must be a public URL — do not pass URLs that require authentication headers.",
						},
						"method": {
							Type:        "string",
							Required:    false,
							Description: "HTTP method to use: 'GET' (default) or 'POST'. Use POST when sending a request body.",
						},
						"headers": {
							Type:        "object",
							Required:    false,
							Description: "Optional HTTP request headers as key-value pairs (e.g. {'Accept': 'application/json'}). Do not include Authorization or credential headers.",
						},
						"body": {
							Type:        "string",
							Required:    false,
							Description: "Request body for POST requests. Must be a string. For JSON payloads, set Content-Type header to 'application/json'.",
						},
					},
				},
			},

			"web.search": {
				Name:                    "web.search",
				Level:                   "command",
				Label:                   "Web Search (Tavily)",
				Description:             "Search the web using the Tavily AI Search API. Returns ranked results with title, URL, and content snippet. Do not use for fetching a specific URL — use web.fetch for that.",
				TimeoutSeconds:          60,
				RequiredCredentialTypes: []string{"search_api_key"},
				Spec: &types.SkillSpec{
					Parameters: map[string]types.ParameterDef{
						"query": {
							Type:        "string",
							Required:    true,
							Description: "Plain-language search query. Be specific and descriptive for best results. Do not use boolean operators or special syntax.",
						},
						"max_results": {
							Type:        "integer",
							Required:    false,
							Description: "Maximum number of results to return (1–20). Defaults to 5. Use a higher value only when breadth is more important than speed.",
						},
						"include_domains": {
							Type:        "array",
							Required:    false,
							Description: "Restrict results to these domains (e.g. ['docs.python.org', 'github.com']). Omit to search the open web.",
						},
						"exclude_domains": {
							Type:        "array",
							Required:    false,
							Description: "Exclude results from these domains. Omit to apply no domain exclusions.",
						},
					},
				},
			},

			"web.extract": {
				Name:                    "web.extract",
				Level:                   "command",
				Label:                   "Web Content Extraction",
				Description:             "Fetch a public URL and return its content as clean text or Markdown, stripping HTML boilerplate. Do not use for binary files or URLs requiring authentication.",
				TimeoutSeconds:          60,
				RequiredCredentialTypes: []string{},
				Spec: &types.SkillSpec{
					Parameters: map[string]types.ParameterDef{
						"url": {
							Type:        "string",
							Required:    true,
							Description: "The full URL of the page to extract content from (must be http:// or https://). Must be publicly accessible.",
						},
						"format": {
							Type:        "string",
							Required:    false,
							Description: "Output format: 'text' (default) returns plain text; 'markdown' preserves headings and links; 'links' returns only the list of hyperlinks found on the page.",
						},
					},
				},
			},
		},
	}
}
