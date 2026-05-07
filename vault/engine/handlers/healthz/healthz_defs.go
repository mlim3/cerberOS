package healthz

// HealthResponse is the JSON body for the health check response.
type HealthzResponse struct {
	Status string `json:"status"`
}
