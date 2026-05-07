package validation

import (
	"aegis-databus/pkg/envelope"
	"aegis-databus/pkg/security"
)

// StrictValidator enforces CloudEvents and subject-level ACL.
// Default for production.
var Strict Validator = &StrictValidator{}

// StrictValidator implements Validator with CloudEvents + ACL.
type StrictValidator struct{}

func (*StrictValidator) ValidatePayload(payload []byte) error {
	return envelope.Validate(payload)
}

func (*StrictValidator) ValidatePublish(component, subject string, payload []byte) error {
	if err := security.CheckPublish(component, subject); err != nil {
		return err
	}
	return envelope.Validate(payload)
}

func (*StrictValidator) ValidateSubscribe(component, subject string) error {
	return security.CheckSubscribe(component, subject)
}
