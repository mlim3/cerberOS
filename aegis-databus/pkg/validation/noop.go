package validation

// NoOp skips all validation. Use in tests.
var NoOp Validator = &NoOpValidator{}

// NoOpValidator implements Validator with no checks.
type NoOpValidator struct{}

func (*NoOpValidator) ValidatePayload([]byte) error                          { return nil }
func (*NoOpValidator) ValidatePublish(_, _ string, _ []byte) error           { return nil }
func (*NoOpValidator) ValidateSubscribe(_, _ string) error { return nil }
