package m

import (
	"encoding/json"
	"time"
	"unsafe"

	"example.com/m/contract"
)

// Typed carries only concrete fields: nothing is reported.
type Typed struct {
	contract.EventMeta
	ID       string
	Count    int
	Tags     []string
	Labels   map[string]string
	At       time.Time
	Nested   Inner
	Hidden   any `json:"-"`
	internal any
}

func (*Typed) Name() string { return "typed" }

// Inner is a concrete struct walked from a field.
type Inner struct{ Code int }

// Untyped embeds EventMeta and has no Name method: an event all the same.
type Untyped struct {
	contract.EventMeta
	Value   interface{}      // want field
	List    []any            // want field
	Map     map[string]any   // want field
	Keys    map[any]string   // want field
	Ptr     *any             // want field
	Arr     [2]any           // want field
	Deep    DeepHolder       // want field
	Fn      func()           // want field
	Ch      chan int         // want field
	Raw     unsafe.Pointer   // want field
	Stringy interface{ S() } // want field
	Err     error            // want field
	Codec   OwnCodec
	Text    OwnText
	Msg     json.RawMessage
}

// DeepHolder holds an interface two levels down.
type DeepHolder struct{ Inner struct{ V any } }

// OwnCodec owns its JSON codec: a leaf whatever it holds.
type OwnCodec struct{ V any }

func (OwnCodec) MarshalJSON() ([]byte, error) { return nil, nil }
func (*OwnCodec) UnmarshalJSON([]byte) error  { return nil }

// OwnText owns its text codec: a leaf.
type OwnText struct{ V any }

func (OwnText) MarshalText() ([]byte, error) { return nil, nil }
func (*OwnText) UnmarshalText([]byte) error  { return nil }

// WithErrCodec has the error codec pair: its error field is carried as
// text, but an interface field is still reported.
type WithErrCodec struct {
	contract.EventMeta
	Err   error
	Other any // want field
}

func (WithErrCodec) MarshalJSON() ([]byte, error) { return nil, nil }
func (*WithErrCodec) UnmarshalJSON([]byte) error  { return nil }

// Named has only a Name method (no envelope): an event.
type Named struct {
	Payload any // want field
}

func (Named) Name() string { return "named" }

// Base is embedded by a user event: its fields are the event's.
type Base struct {
	EventName string
	Model     any // want field
}

func (b *Base) Name() string { return b.EventName }

// Embedding gets its Name from Base and is walked through it.
type Embedding struct {
	Base
	Action string
}

// Generic holds a type parameter.
type Generic[T any] struct {
	contract.EventMeta
	V T // want field
}

// notExported is not an event of the public surface.
type notExported struct {
	contract.EventMeta
	V any
}

// NotAnEvent has neither a Name method nor the envelope.
type NotAnEvent struct{ V any }

// WrongName has a Name method of another shape.
type WrongName struct{ V any }

func (WrongName) Name(int) string { return "" }

var _ = notExported{}
