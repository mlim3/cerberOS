// Package websearch implements the SearchProvider interface backed by the
// Tavily AI search API (api.tavily.com). It is the only package in the vault
// engine that makes outbound HTTP calls for search operations.
package websearch

// SearchParams holds the caller-supplied parameters for a web search operation.
type SearchParams struct {
	Query          string   `json:"query"`
	MaxResults     int      `json:"max_results,omitempty"`
	IncludeDomains []string `json:"include_domains,omitempty"`
	ExcludeDomains []string `json:"exclude_domains,omitempty"`
}

// SearchResult is the structured response returned to the caller after a
// successful search. It contains only operation output — the Tavily API key
// used to perform the search is never included.
type SearchResult struct {
	Query   string             `json:"query"`
	Results []SearchResultItem `json:"results"`
}

// SearchResultItem is one ranked result returned by the search provider.
type SearchResultItem struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

// tavilyRequest is the wire format sent to api.tavily.com/search.
// The APIKey field is populated from the vault's SecretManager and must
// never be logged, returned to callers, or included in audit events.
type tavilyRequest struct {
	APIKey         string   `json:"api_key"`
	Query          string   `json:"query"`
	MaxResults     int      `json:"max_results"`
	IncludeDomains []string `json:"include_domains,omitempty"`
	ExcludeDomains []string `json:"exclude_domains,omitempty"`
}

// tavilyResponse is the wire format received from api.tavily.com/search.
type tavilyResponse struct {
	Query   string             `json:"query"`
	Results []tavilyResultItem `json:"results"`
	Error   string             `json:"detail,omitempty"` // Tavily sends errors as {"detail":"..."}
}

type tavilyResultItem struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}
