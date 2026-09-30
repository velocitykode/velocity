package m

import "example.com/m/contract"

// Enveloped is built by the module and carries the envelope.
type Enveloped struct {
	contract.EventMeta
	Action string
}

func (Enveloped) Name() string { return "enveloped" }

// Bare is built by the module without the envelope.
type Bare struct { // want envelope
	Action string
}

func (Bare) Name() string { return "bare" }

// BareByNew is built by new, without the envelope.
type BareByNew struct { // want envelope
	Action string
}

func (*BareByNew) Name() string { return "bare.new" }

// Embedder carries a base event and the envelope; its literal fills Base,
// which is not thereby an event the module builds.
type Embedder struct {
	contract.EventMeta
	Base
	Action string
}

// Unbuilt has no envelope and the module never builds it: a type for
// applications to use.
type Unbuilt struct {
	Action string
}

func (Unbuilt) Name() string { return "unbuilt" }

func build() []any {
	return []any{
		Enveloped{Action: "a"},
		&Bare{Action: "b"},
		new(BareByNew),
		&Embedder{Base: Base{EventName: "e"}, Action: "c"},
	}
}

var _ = build
