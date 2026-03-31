package secrets

// SecretGetRequest is the JSON body for POST /secrets/get.
type SecretGetRequest struct {
	Agent string   `json:"agent"`
	Keys  []string `json:"keys"`
}

// SecretGetResponse is the JSON body for a successful POST /secrets/get.
type SecretGetResponse struct {
	Secrets map[string]string `json:"secrets"`
}

// SecretPutRequest is the JSON body for POST /secrets/put.
type SecretPutRequest struct {
	Agent string `json:"agent"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

// SecretDeleteRequest is the JSON body for POST /secrets/delete.
type SecretDeleteRequest struct {
	Agent string `json:"agent"`
	Key   string `json:"key"`
}
