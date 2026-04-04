package inject

// InjectRequest is the JSON body for POST /inject.
type InjectRequest struct {
	Agent  string   `json:"agent"`
	Script string   `json:"script"`
	Keys   []string `json:"keys"`
}

// InjectResponse is the JSON body for a successful POST /inject.
type InjectResponse struct {
	Agent  string `json:"agent"`
	Script string `json:"script"`
}
