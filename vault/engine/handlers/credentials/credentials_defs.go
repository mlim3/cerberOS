package credentials

// CredentialPutRequest is the JSON body for POST /credentials/put.
type CredentialPutRequest struct {
	UserID         string `json:"user_id"`
	CredentialType string `json:"credential_type"`
	Value          string `json:"value"`
}

// CredentialGetRequest is the JSON body for POST /credentials/get (internal only).
type CredentialGetRequest struct {
	UserID         string `json:"user_id"`
	CredentialType string `json:"credential_type"`
}

// CredentialGetResponse is the JSON body for a successful POST /credentials/get.
type CredentialGetResponse struct {
	Value string `json:"value"`
}

// CredentialDeleteRequest is the JSON body for POST /credentials/delete.
type CredentialDeleteRequest struct {
	UserID         string `json:"user_id"`
	CredentialType string `json:"credential_type"`
}
