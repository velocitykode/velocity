package velocity

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// eventEnvelopeType names the envelope every framework event embeds.
const eventEnvelopeType = "github.com/velocitykode/velocity/contract.EventMeta"

// eventEnvelopeFields are the envelope's fields, in order.
var eventEnvelopeFields = []string{"Context", "TraceID", "SpanID", "ParentID", "At"}

var (
	errorType    = reflect.TypeFor[error]()
	timeType     = reflect.TypeFor[time.Time]()
	durationType = reflect.TypeFor[time.Duration]()
)

// eventStructType returns the struct type behind an event value.
func eventStructType(event any) reflect.Type {
	t := reflect.TypeOf(event)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// envelopeField returns the event's embedded envelope field, if any.
func envelopeField(st reflect.Type) (reflect.StructField, bool) {
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if f.Anonymous && f.Type.PkgPath()+"."+f.Type.Name() == eventEnvelopeType {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

// ownFields returns the event's fields other than the embedded envelope.
func ownFields(st reflect.Type) []reflect.StructField {
	var out []reflect.StructField
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if f.Anonymous && f.Type.PkgPath()+"."+f.Type.Name() == eventEnvelopeType {
			continue
		}
		out = append(out, f)
	}
	return out
}

// TestFrameworkEvents_EmbedOneEnvelope requires every framework event to
// embed contract.EventMeta, whose fields are the context, the trace, span
// and parent ids and the time, and to hand it back through Meta.
func TestFrameworkEvents_EmbedOneEnvelope(t *testing.T) {
	for _, fe := range frameworkEvents {
		key := eventTypeKey(fe.event)
		st := eventStructType(fe.event)
		f, ok := envelopeField(st)
		if !ok {
			t.Errorf("%s does not embed %s", key, eventEnvelopeType)
			continue
		}
		var names []string
		for i := 0; i < f.Type.NumField(); i++ {
			names = append(names, f.Type.Field(i).Name)
		}
		if !slices.Equal(names, eventEnvelopeFields) {
			t.Errorf("%s: envelope fields %v, want %v", key, names, eventEnvelopeFields)
			continue
		}

		v := reflect.New(st)
		v.Elem().FieldByIndex(f.Index).FieldByName("TraceID").SetString("trace-of-" + key)
		// A value-dispatched event reads its envelope from the value too.
		for _, recv := range []reflect.Value{v, v.Elem()} {
			m := recv.MethodByName("Meta")
			if !m.IsValid() {
				t.Errorf("%s (%s) has no Meta method", key, recv.Type())
				continue
			}
			if got := m.Call(nil)[0].FieldByName("TraceID").String(); got != "trace-of-"+key {
				t.Errorf("%s (%s).Meta().TraceID = %q, want the embedded value", key, recv.Type(), got)
			}
		}
	}
}

// TestFrameworkEvents_EncodeEachFactOneWay checks the fields each event adds
// to the envelope: an error is an error value (never its text), a duration
// is a time.Duration (never milliseconds), the envelope's At is the one
// timestamp, correlation lives only in the envelope, every failure carries
// Err, and the completed and failed events of a run that has a started
// event carry Duration.
func TestFrameworkEvents_EncodeEachFactOneWay(t *testing.T) {
	started := map[string]bool{}
	for _, fe := range frameworkEvents {
		if subject, ok := strings.CutSuffix(fe.event.Name(), ".started"); ok {
			started[subject] = true
		}
	}
	envelopeOnly := map[string]bool{}
	for _, name := range eventEnvelopeFields {
		envelopeOnly[name] = true
	}

	for _, fe := range frameworkEvents {
		key := eventTypeKey(fe.event)
		name := fe.event.Name()
		st := eventStructType(fe.event)
		fields := map[string]reflect.StructField{}
		for _, f := range ownFields(st) {
			fields[f.Name] = f
			switch {
			case envelopeOnly[f.Name]:
				t.Errorf("%s declares %s beside the envelope's", key, f.Name)
			case (f.Name == "Error" || strings.HasSuffix(f.Name, "Err")) && f.Type != errorType:
				t.Errorf("%s.%s is %s, want error", key, f.Name, f.Type)
			case strings.HasSuffix(f.Name, "Ms"):
				t.Errorf("%s.%s counts milliseconds, want a time.Duration", key, f.Name)
			case f.Type == timeType:
				t.Errorf("%s.%s is a second timestamp, want the envelope's At", key, f.Name)
			case f.Name == "Duration" && f.Type != durationType:
				t.Errorf("%s.Duration is %s, want time.Duration", key, f.Type)
			}
		}

		if strings.HasSuffix(name, ".failed") {
			if f, ok := fields["Err"]; !ok || f.Type != errorType {
				t.Errorf("%s (%s) is a failure without an Err error field", key, name)
			}
		}
		dot := strings.LastIndex(name, ".")
		subject, verb := name[:dot], name[dot+1:]
		if (verb == "completed" || verb == "failed") && started[subject] {
			if f, ok := fields["Duration"]; !ok || f.Type != durationType {
				t.Errorf("%s (%s) ends a run with a started event but has no Duration", key, name)
			}
		}
	}
}

// TestFrameworkEvents_JSONFormRoundTrips requires the JSON form of every
// framework event to carry each error field as its text, for the event and
// for a pointer to it, and to decode back into the event type (as a queued
// listener in another process receives it) with each error field holding
// that text. The envelope's Context stays out of the JSON form.
func TestFrameworkEvents_JSONFormRoundTrips(t *testing.T) {
	for _, fe := range frameworkEvents {
		key := eventTypeKey(fe.event)
		st := eventStructType(fe.event)
		v := reflect.New(st)
		if f, ok := envelopeField(st); ok {
			v.Elem().FieldByIndex(f.Index).FieldByName("Context").Set(reflect.ValueOf(context.Background()))
		} else if c := v.Elem().FieldByName("Context"); c.IsValid() {
			c.Set(reflect.ValueOf(context.Background()))
		}
		want := map[string]string{}
		for _, f := range ownFields(st) {
			if f.Type != errorType {
				continue
			}
			text := "the " + f.Name + " of " + key
			v.Elem().FieldByIndex(f.Index).Set(reflect.ValueOf(errors.New(text)))
			jsonKey := f.Name
			if tag, _, _ := strings.Cut(f.Tag.Get("json"), ","); tag != "" {
				jsonKey = tag
			}
			want[jsonKey] = text
		}
		for _, form := range []any{v.Interface(), v.Elem().Interface()} {
			raw, err := json.Marshal(form)
			if err != nil {
				t.Errorf("json.Marshal(%T): %v", form, err)
				continue
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Errorf("%T: decode %s: %v", form, raw, err)
				continue
			}
			if _, ok := got["Context"]; ok {
				t.Errorf("%T: JSON form carries the Context: %s", form, raw)
			}
			for jsonKey, text := range want {
				if got[jsonKey] != text {
					t.Errorf("%T: JSON %q = %v, want %q (from %s)", form, jsonKey, got[jsonKey], text, raw)
				}
			}

			back := reflect.New(st)
			if err := json.Unmarshal(raw, back.Interface()); err != nil {
				t.Errorf("%s: decode its JSON form %s: %v", key, raw, err)
				continue
			}
			for _, f := range ownFields(st) {
				if f.Type != errorType {
					continue
				}
				got, _ := back.Elem().FieldByIndex(f.Index).Interface().(error)
				if got == nil || !strings.HasPrefix(got.Error(), "the "+f.Name+" of ") {
					t.Errorf("%s: decoded %s = %v, want its text", key, f.Name, got)
				}
			}
		}
	}
}

// TestFrameworkEvents_FieldsHydrateFromJSON requires every field of every
// framework event to be one json.Unmarshal can rebuild, so a queued
// listener in another process can hydrate the event: no field (or element
// of one) is an interface with methods, other than error, which the error
// codec carries as its text.
func TestFrameworkEvents_FieldsHydrateFromJSON(t *testing.T) {
	var check func(key, path string, ft reflect.Type, seen map[reflect.Type]bool)
	check = func(key, path string, ft reflect.Type, seen map[reflect.Type]bool) {
		switch ft.Kind() {
		case reflect.Interface:
			if ft != errorType && ft.NumMethod() > 0 {
				t.Errorf("%s: field %s is %s, an interface json.Unmarshal cannot rebuild", key, path, ft)
			}
		case reflect.Pointer, reflect.Slice, reflect.Array:
			check(key, path+"[]", ft.Elem(), seen)
		case reflect.Map:
			check(key, path+"[key]", ft.Key(), seen)
			check(key, path+"[]", ft.Elem(), seen)
		case reflect.Struct:
			if seen[ft] || ft == timeType {
				return
			}
			seen[ft] = true
			for i := 0; i < ft.NumField(); i++ {
				if f := ft.Field(i); f.IsExported() {
					check(key, path+"."+f.Name, f.Type, seen)
				}
			}
		}
	}
	for _, fe := range frameworkEvents {
		key := eventTypeKey(fe.event)
		for _, f := range ownFields(eventStructType(fe.event)) {
			if f.IsExported() {
				check(key, f.Name, f.Type, map[reflect.Type]bool{})
			}
		}
	}
}
