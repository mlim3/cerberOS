package api

type ErrorDetails struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

type ResponseEnvelope struct {
	Ok    bool          `json:"ok"`
	Data  any           `json:"data"`
	Error *ErrorDetails `json:"error"`
}

// SuccessResponse creates a standard success response envelope
func SuccessResponse(data any) ResponseEnvelope {
	return ResponseEnvelope{
		Ok:    true,
		Data:  data,
		Error: nil,
	}
}

// ErrorResponse creates a standard error response envelope
func ErrorResponse(code, message string, details any) ResponseEnvelope {
	return ResponseEnvelope{
		Ok:   false,
		Data: nil,
		Error: &ErrorDetails{
			Code:    code,
			Message: message,
			Details: details,
		},
	}
}
