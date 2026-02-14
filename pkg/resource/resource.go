package resource

// Resource is implemented by types that can be transformed into API responses.
// Each implementation defines how a model is serialized for the client.
type Resource interface {
	ToResource() map[string]any
}
