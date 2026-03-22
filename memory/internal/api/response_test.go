package api

import (
	"reflect"
	"testing"
)

func TestResponseEnvelope(t *testing.T) {
	tests := []struct {
		name     string
		isError  bool
		code     string
		message  string
		details  any
		data     any
		expected ResponseEnvelope
	}{
		{
			name:    "Success Response",
			isError: false,
			data:    map[string]string{"foo": "bar"},
			expected: ResponseEnvelope{
				Ok:   true,
				Data: map[string]string{"foo": "bar"},
			},
		},
		{
			name:    "Error Response",
			isError: true,
			code:    "not_found",
			message: "Resource not found",
			details: nil,
			expected: ResponseEnvelope{
				Ok:   false,
				Data: nil,
				Error: &ErrorDetails{
					Code:    "not_found",
					Message: "Resource not found",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got ResponseEnvelope
			if tt.isError {
				got = ErrorResponse(tt.code, tt.message, tt.details)
			} else {
				got = SuccessResponse(tt.data)
			}

			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, got)
			}
		})
	}
}
