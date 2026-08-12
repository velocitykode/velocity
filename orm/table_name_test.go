package orm

import (
	"reflect"
	"testing"

	"github.com/velocitykode/velocity/str"
)

// b7UserProfile is a plain multi-word model with no TableName(): it
// exercises the fallback derivation on every path.
type b7UserProfile struct{}

// b7ValueTable declares TableName() on the VALUE receiver.
type b7ValueTable struct{}

func (b7ValueTable) TableName() string { return "b7_custom_value" }

// b7PtrTable declares TableName() on the POINTER receiver. Before B7 the
// read path (getTableName) probed only the value receiver, so this method
// was invisible to reads while the write path honored it - the two paths
// hit different tables. The unified helper honors it everywhere.
type b7PtrTable struct{}

func (*b7PtrTable) TableName() string { return "b7_custom_ptr" }

// TestTableNameDerivation_Unified_RegressionB7 proves the read path
// (getTableName), the write path (saveWithDriver, which now delegates to
// deriveTableName) and the relation path (resolveTableNameReflect) derive
// the SAME table name for (a) a plain multi-word type, (b) a value-receiver
// TableName, and (c) a pointer-receiver TableName.
func TestTableNameDerivation_Unified_RegressionB7(t *testing.T) {
	cases := []struct {
		name string
		typ  reflect.Type
		read string // getTableName[T]() result, captured at compile time
		want string
	}{
		{
			name: "PlainMultiWord",
			typ:  reflect.TypeOf(b7UserProfile{}),
			read: getTableName[b7UserProfile](),
			// str.Plural(ToSnakeCase("b7UserProfile")) -> "b7_user_profiles".
			// The pre-B7 read path produced "b7userprofiles".
			want: str.Plural(ToSnakeCase("b7UserProfile")),
		},
		{
			name: "ValueReceiverTableName",
			typ:  reflect.TypeOf(b7ValueTable{}),
			read: getTableName[b7ValueTable](),
			want: "b7_custom_value",
		},
		{
			name: "PointerReceiverTableName",
			typ:  reflect.TypeOf(b7PtrTable{}),
			read: getTableName[b7PtrTable](),
			want: "b7_custom_ptr",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// write path: saveWithDriver derives via deriveTableName(t).
			write := deriveTableName(tc.typ)
			// relation path.
			rel := resolveTableNameReflect(tc.typ)

			if tc.read != tc.want || write != tc.want || rel != tc.want {
				t.Fatalf("%s: read=%q write=%q relation=%q, all want %q",
					tc.name, tc.read, write, rel, tc.want)
			}
		})
	}
}

// TestTableNameDerivation_ScaffolderParity_B7 asserts the runtime fallback
// matches the scaffolder's toTableName (console/gen_model.go), which emits
// str.Plural(orm.ToSnakeCase(name)). Asserting the formula directly avoids
// an orm <- console import cycle. UserProfile is the sample the unification
// decision calls out.
func TestTableNameDerivation_ScaffolderParity_B7(t *testing.T) {
	// Local type so its reflect Name is exactly "UserProfile" without
	// colliding with package-level test fixtures.
	type UserProfile struct{}

	const sample = "UserProfile"
	const wantTable = "user_profiles"

	scaffolder := str.Plural(ToSnakeCase(sample))
	if scaffolder != wantTable {
		t.Fatalf("scaffolder formula str.Plural(ToSnakeCase(%q)) = %q, want %q",
			sample, scaffolder, wantTable)
	}

	runtime := deriveTableName(reflect.TypeOf(UserProfile{}))
	if runtime != scaffolder {
		t.Fatalf("runtime derivation %q != scaffolder derivation %q for %q",
			runtime, scaffolder, sample)
	}
}

// TestDeriveTableName_CacheParity proves a custom-TableName type and a
// default-pluralized type both resolve identically whether the call is a
// cold miss (cache cleared) or a warm hit, and that *T and T share an entry.
func TestDeriveTableName_CacheParity(t *testing.T) {
	cases := []struct {
		typ  reflect.Type
		want string
	}{
		{reflect.TypeOf(b7ValueTable{}), "b7_custom_value"},
		{reflect.TypeOf(b7PtrTable{}), "b7_custom_ptr"},
		{reflect.TypeOf(b7UserProfile{}), str.Plural(ToSnakeCase("b7UserProfile"))},
	}
	for _, tc := range cases {
		tableNameCache.Delete(tc.typ) // force cold path
		cold := deriveTableName(tc.typ)
		warm := deriveTableName(tc.typ)                   // cached
		ptr := deriveTableName(reflect.PointerTo(tc.typ)) // deref to same key
		if cold != tc.want || warm != tc.want || ptr != tc.want {
			t.Fatalf("%s: cold=%q warm=%q ptr=%q want %q",
				tc.typ, cold, warm, ptr, tc.want)
		}
	}
}

// BenchmarkDeriveTableName measures the warm (cached) path: the type is
// resolved once before the loop so every iteration is a sync.Map hit.
func BenchmarkDeriveTableName(b *testing.B) {
	t := reflect.TypeOf(b7UserProfile{})
	deriveTableName(t) // warm the cache
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = deriveTableName(t)
	}
}

// BenchmarkDeriveTableNameCold measures the uncached derivation by deleting
// the cache entry each iteration, for contrast with the warm path.
func BenchmarkDeriveTableNameCold(b *testing.B) {
	t := reflect.TypeOf(b7UserProfile{})
	for i := 0; i < b.N; i++ {
		tableNameCache.Delete(t)
		_ = deriveTableName(t)
	}
}
