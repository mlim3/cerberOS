package validation

// Validator encapsulates publish and subscribe validation.
// Keeps DataBus/bus simple; validation logic is pluggable.
type Validator interface {
	// ValidatePayload checks CloudEvents envelope (specversion, id, source, type).
	// Used by PublishValidated when no component context.
	ValidatePayload(payload []byte) error

	// ValidatePublish checks ACL and CloudEvents before publish.
	ValidatePublish(component, subject string, payload []byte) error

	// ValidateSubscribe checks ACL before subscribe (e.g. DLQ admin-only).
	ValidateSubscribe(component, subject string) error
}
