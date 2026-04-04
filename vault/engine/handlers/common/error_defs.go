package common

// ErrorResponse is the JSON body for handler error responses that use application/json.
type ErrorResponse struct {
	Error string `json:"error"`
}
