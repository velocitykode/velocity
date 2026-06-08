package orm

import (
	"time"
)

// Composable trait structs let consumers build a model out of independent
// behaviors. Instead of picking from a fixed cross-product of base types,
// embed the traits that match the table's actual shape:
//
//	type Invoice struct {
//	    orm.IDInt[Invoice]
//	    orm.Timestamps
//	    orm.SoftDeletes[Invoice]
//	    Number string
//	}
//
//	type AuditEntry struct {
//	    orm.IDUUID[AuditEntry]
//	    orm.CreatedAtOnly
//	    orm.AppendOnly
//	    Action string
//	}
//
//	type Snapshot struct {
//	    orm.IDInt[Snapshot]
//	    orm.SoftDeletes[Snapshot]
//	    CreatedAt time.Time `orm:"column:captured_at"`
//	}
//
// Behaviors are detected by an unexported sentinel embedded as the first
// field of every trait struct (see ormTrait* types below). A field that
// happens to be named CreatedAt / UpdatedAt / DeletedAt on the outer
// struct is therefore NOT conflated with the matching trait - detection
// is "embeds this exact trait struct" and only the explicit trait
// composition opts a model in to auto-management.
//
// Existence-tracking (the IsExisting flag a Save needs to decide INSERT
// vs UPDATE) is auto-attached when ANY orm trait is detected, so custom
// compositions never need to remember a separate orm.Existence embed -
// the framework keeps the bit in a side channel keyed weakly by the
// model pointer. See existence.go for the storage strategy.
//
// Convenience compositions (Model[T], UUIDModel[T], SoftDeleteModel[T],
// SoftDeleteUUIDModel[T], ImmutableModel[T], ImmutableUUIDModel[T]) are
// thin trait combinations exported for the most common shapes. They
// have no special meaning to the framework: each is identical to the
// equivalent hand-rolled composition.

type ormTraitIDInt struct{}
type ormTraitIDUUID struct{}
type ormTraitTimestamps struct{}
type ormTraitCreatedAtOnly struct{}
type ormTraitSoftDeletes struct{}
type ormTraitAppendOnly struct{}

// IDInt declares an auto-increment integer primary key (uint). The
// generic parameter T is the concrete model. The static-like query
// helpers (Find, Where, All, Create, ...) are NOT attached to this trait;
// they hang off the convenience compositions (Model[T], SoftDeleteModel[T],
// ...). T is carried here only so those compositions resolve their row
// type to the caller's model. IDInt itself contributes just the id column.
type IDInt[T any] struct {
	_  ormTraitIDInt
	ID uint `orm:"primaryKey;autoIncrement" json:"id"`
}

// IDUUID declares a UUID primary key. The framework generates a v4
// UUID on insert when ID is empty.
type IDUUID[T any] struct {
	_  ormTraitIDUUID
	ID string `orm:"primaryKey;type:uuid" json:"id"`
}

// Timestamps carries the standard CreatedAt / UpdatedAt pair. Override
// the column names by declaring a same-named field on the outer struct
// with an `orm:"column:..."` tag - Go's field promotion makes the outer
// declaration win, and the serialization layer drops the trait's
// emission to avoid double-writing.
type Timestamps struct {
	_         ormTraitTimestamps
	CreatedAt time.Time `orm:"autoCreateTime" json:"created_at"`
	UpdatedAt time.Time `orm:"autoUpdateTime" json:"updated_at"`
}

// TimestampsToggler lets a model that embeds a timestamps-bearing base
// (Model[T], UUIDModel[T], SoftDeleteModel[T], SoftDeleteUUIDModel[T], or a
// hand-rolled orm.Timestamps / orm.CreatedAtOnly composition) opt OUT of
// automatic created_at / updated_at management by declaring:
//
//	func (M) UsesTimestamps() bool { return false }
//
// The method is expected to return a per-type constant. A model that does
// not implement it, or returns true, keeps its timestamps managed - so every
// existing model is unaffected by default. When it returns false the
// framework drops created_at/updated_at from the model's column set
// entirely: inserts never stamp or write them, bulk updates never inject
// updated_at, and a table that has no such columns is fully queryable. The
// soft-delete deleted_at column is a separate concern and is left intact.
//
// Naming note: the method is UsesTimestamps (not Timestamps), because
// Timestamps is the embedded trait's field name and would collide.
type TimestampsToggler interface {
	UsesTimestamps() bool
}

// CreatedAtOnly carries CreatedAt but no UpdatedAt. Use this when the
// table is append-only (audit logs, event ledgers): an accidental
// orm.Save() update path then has no UpdatedAt column to target, and
// the query builder skips updated_at injection on Update().
type CreatedAtOnly struct {
	_         ormTraitCreatedAtOnly
	CreatedAt time.Time `orm:"autoCreateTime" json:"created_at"`
}

// SoftDeletes carries DeletedAt. The query layer auto-installs the
// "deleted_at IS NULL" global scope when this trait is present, so
// soft-deleted rows are hidden from default reads.
//
// Use the WithTrashed() / OnlyTrashed() chain methods (promoted via
// this trait) or Query.WithoutGlobalScope(SoftDeleteScopeName) to
// bypass.
//
// The generic parameter T pairs the trait with the concrete model so
// the soft-delete-specific static helpers (OnlyTrashed, WithTrashed,
// ForceDeleteWhere) return *Query[T].
type SoftDeletes[T any] struct {
	_         ormTraitSoftDeletes
	DeletedAt *time.Time `orm:"index" json:"deleted_at,omitempty"`
}

// AppendOnly is a marker trait. Its presence tells the framework the
// model is append-only:
//   - orm.Save() on a row with IsExisting=true returns ErrImmutableModelUpdate.
//   - Query.Update() skips the updated_at injection.
//
// AppendOnly composes with SoftDeletes: the soft-delete UPDATE that
// only writes deleted_at is permitted on append-only rows so a tombstone
// can be recorded without breaking the no-content-mutation contract.
//
// The marker carries no persistable column. The unexported sentinel
// exists only so reflection can detect the trait via type comparison
// without depending on type names.
type AppendOnly struct {
	_ ormTraitAppendOnly
}

// Existence-state (IsExisting bit, change tracking via Original /
// Changed snapshots) lives entirely in the package-level side-channel
// (existence.go). The previous orm.Existence trait was dropped to
// shrink the trait surface from 7 to 6: Save's INSERT-vs-UPDATE
// decision is automatic via the side-channel for any model carrying
// at least one orm trait, and consumers that want change tracking
// call orm.Track(&m) at load time then orm.IsDirty(&m) /
// orm.HasChanged(&m, "field") / orm.IsClean(&m) / orm.MarkClean(&m)
// for inspection. No embed required either way.
