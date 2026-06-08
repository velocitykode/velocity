package orm

import (
	"fmt"
	"reflect"
	"sync"
)

// modelFeatures is the per-type fingerprint computed by walking a
// model's exported fields (recursively into embedded anonymous structs
// from this package). Every code path that previously matched on
// concrete type names ("orm.Model[", "orm.SoftDeleteModel[", ...)
// now consults this struct instead, which lets arbitrary trait
// compositions work without code-side enumeration.
type modelFeatures struct {
	hasIntPK     bool
	hasUUIDPK    bool
	hasUpdatedAt bool
	hasCreatedAt bool
	hasDeletedAt bool
	appendOnly   bool

	// timestampsOptOut records that the model declared
	// UsesTimestamps() bool returning false (see TimestampsToggler). When
	// set, hasCreatedAt and hasUpdatedAt are forced false above so no write
	// path stamps or injects created_at/updated_at, and the save layer
	// skips the in-memory field stamping. deleted_at is unaffected.
	timestampsOptOut bool

	// hasAnyTrait records whether at least one orm trait sentinel was
	// found. Used as the trigger for implicit existence tracking: a
	// model with no traits at all is plain data, not an orm-managed row.
	hasAnyTrait bool
}

// hasPK reports whether the model carries an ID column managed by a
// PK trait. A custom struct that declares ID directly (without a PK
// trait) is NOT detected as a PK here.
func (f modelFeatures) hasPK() bool { return f.hasIntPK || f.hasUUIDPK }

// featureCache memoizes modelFeatures by reflect.Type. Detection
// involves a recursive field walk; the per-type result is invariant.
var featureCache sync.Map

// FeaturesError reports a detection-time failure (mutually exclusive
// trait combinations, malformed trait composition). Library code MUST
// NOT panic on these; callers convert to ordinary errors that surface
// at the request that triggered detection.
//
// Consumers can opt in to startup-time validation via RegisterModel[T]()
// which calls featuresFor eagerly and returns the same error type.
type FeaturesError struct {
	Model  string
	Reason string
}

func (e *FeaturesError) Error() string {
	return fmt.Sprintf("orm: invalid trait composition on %s: %s", e.Model, e.Reason)
}

// featuresFor returns the cached modelFeatures for t (or computes and
// caches them on first call). Pointer types are dereferenced.
//
// Returns *FeaturesError on detection failure (mutually exclusive trait
// combinations); callers propagate the error rather than panicking.
// Detection is invariant per type so the error fires once at first save
// (or query construction) timing, and the failure is cached so repeated
// calls don't re-walk.
func featuresFor(t reflect.Type) (modelFeatures, error) {
	if t == nil {
		return modelFeatures{}, nil
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if cached, ok := featureCache.Load(t); ok {
		switch v := cached.(type) {
		case modelFeatures:
			return v, nil
		case *FeaturesError:
			return modelFeatures{}, v
		}
	}
	feats, err := detectFeatures(t)
	if err != nil {
		featureCache.Store(t, err)
		return modelFeatures{}, err
	}
	featureCache.Store(t, feats)
	return feats, nil
}

// featuresForT is the typed entry point. T must be a concrete struct
// (or a pointer to one); an interface T whose zero value is nil yields
// reflect.TypeOf(zero) == nil and is rejected with a *FeaturesError so
// the caller does not silently fall through to a no-traits fingerprint.
func featuresForT[T any]() (modelFeatures, error) {
	var zero T
	t := reflect.TypeOf(zero)
	if t == nil {
		return modelFeatures{}, &FeaturesError{
			Model:  "<nil>",
			Reason: "T is an interface or untyped nil; orm requires a concrete struct type for fingerprinting",
		}
	}
	return featuresFor(t)
}

// RegisterModel eagerly validates the trait composition for T and
// caches the result. Call from a provider Boot() or main() to surface
// trait misconfiguration at startup rather than at first request.
//
// Returns *FeaturesError if the composition is invalid (e.g. dual
// primary-key traits or both Timestamps and CreatedAtOnly embedded).
func RegisterModel[T any]() error {
	_, err := featuresForT[T]()
	return err
}

// MustRegisterModel is RegisterModel that panics on failure. Use only
// at program startup where a misconfigured model is unrecoverable.
func MustRegisterModel[T any]() {
	if err := RegisterModel[T](); err != nil {
		panic(err)
	}
}

// traitClassification is the intermediate result of inspecting one
// anonymous embedded struct: which trait sentinel it carries (if any).
type traitClassification int

const (
	traitNone traitClassification = iota
	traitIDInt
	traitIDUUID
	traitTimestamps
	traitCreatedAtOnly
	traitSoftDeletes
	traitAppendOnly
)

// classifyTrait inspects the type of an anonymous embedded field and
// reports which trait sentinel it carries, if any. Inspection is by
// the type of the first field on t (the unexported sentinel) rather
// than t's name, so renaming the trait (or generating it) does not
// break detection.
func classifyTrait(t reflect.Type) traitClassification {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || t.NumField() == 0 {
		return traitNone
	}
	first := t.Field(0)
	switch first.Type {
	case reflect.TypeOf(ormTraitIDInt{}):
		return traitIDInt
	case reflect.TypeOf(ormTraitIDUUID{}):
		return traitIDUUID
	case reflect.TypeOf(ormTraitTimestamps{}):
		return traitTimestamps
	case reflect.TypeOf(ormTraitCreatedAtOnly{}):
		return traitCreatedAtOnly
	case reflect.TypeOf(ormTraitSoftDeletes{}):
		return traitSoftDeletes
	case reflect.TypeOf(ormTraitAppendOnly{}):
		return traitAppendOnly
	}
	return traitNone
}

// detectFeatures walks the outer struct's fields and recurses into
// anonymous embedded structs whose type lives in the orm package. Each
// embedded trait is classified by its leading unexported sentinel
// type, so only struct types defined in this package (and only those
// that opted in by including a sentinel) are recognized as traits.
//
// Mutually-exclusive trait combinations (two PK traits, both
// Timestamps and CreatedAtOnly) return *FeaturesError. Detection is
// per-type and the error is cached, so repeated calls don't re-walk.
//
// Non-orm embedded structs are NOT recursed.
func detectFeatures(t reflect.Type) (modelFeatures, error) {
	if t.Kind() != reflect.Struct {
		return modelFeatures{}, nil
	}
	feats := modelFeatures{}
	if err := walkTraits(t, nil, &feats, t.String()); err != nil {
		return modelFeatures{}, err
	}
	// Per-model timestamps opt-out: a model that declares
	// UsesTimestamps() bool returning false keeps its PK and soft-delete
	// behavior but drops auto-managed created_at/updated_at. Clearing the
	// flags here is the single point that makes the bulk-Update injection
	// (q.hasUpdatedAt) and the save-path stamping honor the opt-out.
	if modelOptsOutOfTimestamps(t) {
		feats.timestampsOptOut = true
		feats.hasCreatedAt = false
		feats.hasUpdatedAt = false
	}
	return feats, nil
}

// modelOptsOutOfTimestamps reports whether the model type t declares
// UsesTimestamps() bool returning false (see TimestampsToggler). The method
// returns a per-type constant, so calling it on a fresh zero value is safe
// and the result is invariant - callers cache it via the per-type meta and
// feature caches. A type that does not implement the method (the default)
// manages timestamps as usual, so existing models are unaffected.
//
// A pointer to a fresh zero value is used for the assertion so the method is
// found whether it has a value or pointer receiver.
func modelOptsOutOfTimestamps(t reflect.Type) bool {
	if t == nil {
		return false
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return false
	}
	if m, ok := reflect.New(t).Interface().(TimestampsToggler); ok {
		return !m.UsesTimestamps()
	}
	return false
}

// walkTraits is the recursion engine for detectFeatures. It walks
// anonymous embedded fields, classifying each by sentinel and
// recursing into orm-package structs that don't directly carry a
// sentinel (the convenience compositions). path is the field-index
// chain from the outer struct to the current type, used to record the
// existencePath when an Existence trait is detected.
//
// Returns *FeaturesError on first conflict, halting the walk.
func walkTraits(t reflect.Type, path []int, feats *modelFeatures, outerName string) error {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.Anonymous {
			continue
		}
		ft := f.Type
		if ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		if ft.Kind() != reflect.Struct {
			continue
		}
		if !isFrameworkType(ft) {
			continue
		}
		childPath := append(append([]int{}, path...), i)
		switch classifyTrait(ft) {
		case traitIDInt:
			if feats.hasIntPK || feats.hasUUIDPK {
				return &FeaturesError{Model: outerName, Reason: "embeds multiple primary-key traits (orm.IDInt and orm.IDUUID); a model may carry only one PK trait"}
			}
			feats.hasIntPK = true
			feats.hasAnyTrait = true
		case traitIDUUID:
			if feats.hasIntPK || feats.hasUUIDPK {
				return &FeaturesError{Model: outerName, Reason: "embeds multiple primary-key traits (orm.IDInt and orm.IDUUID); a model may carry only one PK trait"}
			}
			feats.hasUUIDPK = true
			feats.hasAnyTrait = true
		case traitTimestamps:
			if feats.hasCreatedAt && !feats.hasUpdatedAt {
				return &FeaturesError{Model: outerName, Reason: "embeds both orm.Timestamps and orm.CreatedAtOnly; pick one (Timestamps adds CreatedAt+UpdatedAt, CreatedAtOnly adds CreatedAt only)"}
			}
			feats.hasCreatedAt = true
			feats.hasUpdatedAt = true
			feats.hasAnyTrait = true
		case traitCreatedAtOnly:
			if feats.hasUpdatedAt {
				return &FeaturesError{Model: outerName, Reason: "embeds both orm.Timestamps and orm.CreatedAtOnly; pick one (Timestamps adds CreatedAt+UpdatedAt, CreatedAtOnly adds CreatedAt only)"}
			}
			feats.hasCreatedAt = true
			feats.hasAnyTrait = true
		case traitSoftDeletes:
			feats.hasDeletedAt = true
			feats.hasAnyTrait = true
		case traitAppendOnly:
			feats.appendOnly = true
			feats.hasAnyTrait = true
		case traitNone:
			if err := walkTraits(ft, childPath, feats, outerName); err != nil {
				return err
			}
		}
	}
	return nil
}

// isFrameworkType reports whether t is a struct type defined in this
// package. Used to gate trait recursion (we walk into orm.* embeds
// but not into a consumer's own embedded structs).
func isFrameworkType(t reflect.Type) bool {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return false
	}
	return t.PkgPath() == ormPackagePath
}

// ormPackagePath is captured once via reflect on a known-orm type so
// the package path stays in lock-step with the actual import path.
var ormPackagePath = reflect.TypeOf(Timestamps{}).PkgPath()
