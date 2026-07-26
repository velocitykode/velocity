package drivers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// Differential tests for argument conversion.
//
// database/sql picks one of four conversion routes from the interfaces a
// statement and connection satisfy (driverArgsConnLocked): default only;
// named-value checker then column converter then default; named-value checker
// then default; column converter then default. Wrapping a driver changes which
// interfaces the sql package sees, so the wrapper can silently move a driver
// onto a different route and convert arguments differently from the driver
// alone.
//
// These tests run the same arguments through the same fake driver twice - once
// unwrapped via sql.Open, once wrapped via openInstrumented - across every
// combination of the relevant interfaces, and require the driver to observe
// byte-identical arguments both times.

// recordedArg is one bound argument as the driver finally saw it.
type recordedArg struct {
	Name    string
	Ordinal int
	Value   driver.Value
}

// argRecorder collects the arguments a fake statement received.
type argRecorder struct {
	mu   sync.Mutex
	args []recordedArg
}

func (r *argRecorder) record(nvs []driver.NamedValue) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, nv := range nvs {
		r.args = append(r.args, recordedArg{Name: nv.Name, Ordinal: nv.Ordinal, Value: nv.Value})
	}
}

func (r *argRecorder) snapshot() []recordedArg {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedArg(nil), r.args...)
}

// recorders maps a DSN to the recorder the fake driver writes into, so the
// native and wrapped runs of one case stay separate.
var (
	recordersMu sync.Mutex
	recorders   = map[string]*argRecorder{}
)

func registerRecorder(dsn string) *argRecorder {
	r := &argRecorder{}
	recordersMu.Lock()
	defer recordersMu.Unlock()
	recorders[dsn] = r
	return r
}

func lookupRecorder(dsn string) *argRecorder {
	recordersMu.Lock()
	defer recordersMu.Unlock()
	return recorders[dsn]
}

// stmtKind selects which optional conversion interfaces the fake statement
// implements.
type stmtKind int

const (
	stmtPlain stmtKind = iota
	stmtNVC
	stmtCC
	stmtNVCAndCC
)

func (k stmtKind) String() string {
	switch k {
	case stmtNVC:
		return "stmt=NamedValueChecker"
	case stmtCC:
		return "stmt=ColumnConverter"
	case stmtNVCAndCC:
		return "stmt=NamedValueChecker+ColumnConverter"
	default:
		return "stmt=plain"
	}
}

// fakeDriver opens connections whose statements expose the interface
// combination under test.
type fakeDriver struct {
	kind    stmtKind
	connNVC bool
}

func (d fakeDriver) Open(dsn string) (driver.Conn, error) {
	c := &fakeConn{rec: lookupRecorder(dsn), kind: d.kind}
	if d.connNVC {
		return &fakeConnNVC{fakeConn: c}, nil
	}
	return c, nil
}

// fakeConn deliberately implements neither Queryer nor Execer, so every
// statement goes through Prepare and exercises the prepared-statement
// conversion path.
type fakeConn struct {
	rec  *argRecorder
	kind stmtKind
}

func (c *fakeConn) Prepare(query string) (driver.Stmt, error) {
	base := &fakeStmt{rec: c.rec, nin: strings.Count(query, "?")}
	switch c.kind {
	case stmtNVC:
		return &fakeStmtNVC{fakeStmt: base}, nil
	case stmtCC:
		return &fakeStmtCC{fakeStmt: base}, nil
	case stmtNVCAndCC:
		return &fakeStmtNVCAndCC{fakeStmt: base}, nil
	default:
		return base, nil
	}
}

func (c *fakeConn) Close() error              { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) { return nil, fmt.Errorf("not supported") }

// fakeConnNVC adds a connection-level checker, which database/sql consults
// only when the statement has none.
type fakeConnNVC struct {
	*fakeConn
}

func (c *fakeConnNVC) CheckNamedValue(nv *driver.NamedValue) error {
	if s, ok := nv.Value.(string); ok && s == "conn" {
		nv.Value = "converted-by-conn-nvc"
		return nil
	}
	return driver.ErrSkip
}

type fakeStmt struct {
	rec *argRecorder
	nin int
}

func (s *fakeStmt) Close() error  { return nil }
func (s *fakeStmt) NumInput() int { return s.nin }

func (s *fakeStmt) Exec(args []driver.Value) (driver.Result, error) {
	nvs := make([]driver.NamedValue, len(args))
	for i, v := range args {
		nvs[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	s.rec.record(nvs)
	return driver.RowsAffected(0), nil
}

func (s *fakeStmt) Query(args []driver.Value) (driver.Rows, error) {
	return nil, fmt.Errorf("not supported")
}

func (s *fakeStmt) ExecContext(_ context.Context, args []driver.NamedValue) (driver.Result, error) {
	s.rec.record(args)
	return driver.RowsAffected(0), nil
}

// fakeStmtNVC converts the sentinel "nvc" and declines everything else, so the
// declined arguments exercise whatever retry route the sql package chooses.
type fakeStmtNVC struct {
	*fakeStmt
}

func (s *fakeStmtNVC) CheckNamedValue(nv *driver.NamedValue) error {
	if str, ok := nv.Value.(string); ok && str == "nvc" {
		nv.Value = "converted-by-stmt-nvc"
		return nil
	}
	return driver.ErrSkip
}

// fakeStmtCC tags every value it converts with its column index, making the
// route observable in the recorded arguments.
type fakeStmtCC struct {
	*fakeStmt
}

func (s *fakeStmtCC) ColumnConverter(idx int) driver.ValueConverter {
	return taggingConverter{idx: idx}
}

type fakeStmtNVCAndCC struct {
	*fakeStmt
}

func (s *fakeStmtNVCAndCC) CheckNamedValue(nv *driver.NamedValue) error {
	if str, ok := nv.Value.(string); ok && str == "nvc" {
		nv.Value = "converted-by-stmt-nvc"
		return nil
	}
	return driver.ErrSkip
}

func (s *fakeStmtNVCAndCC) ColumnConverter(idx int) driver.ValueConverter {
	return taggingConverter{idx: idx}
}

type taggingConverter struct{ idx int }

func (c taggingConverter) ConvertValue(v any) (driver.Value, error) {
	return fmt.Sprintf("cc[%d]:%v", c.idx, v), nil
}

// valuerArg exercises the driver.Valuer resolution that database/sql's column
// converter performs before handing a value to ColumnConverter. A wrapper that
// calls ColumnConverter directly skips this step.
type valuerArg struct{ s string }

func (v valuerArg) Value() (driver.Value, error) { return "valued:" + v.s, nil }

var registerFakeDrivers sync.Once

func fakeDriverName(kind stmtKind, connNVC bool) string {
	return fmt.Sprintf("velocity-fake-%d-%t", kind, connNVC)
}

func ensureFakeDrivers() {
	registerFakeDrivers.Do(func() {
		for _, kind := range []stmtKind{stmtPlain, stmtNVC, stmtCC, stmtNVCAndCC} {
			for _, connNVC := range []bool{false, true} {
				sql.Register(fakeDriverName(kind, connNVC), fakeDriver{kind: kind, connNVC: connNVC})
			}
		}
	})
}

// TestInstrumentedConn_ArgumentConversionMatchesUnwrapped is the differential
// check: for every interface combination, the wrapped driver must receive the
// exact arguments the unwrapped driver receives.
func TestInstrumentedConn_ArgumentConversionMatchesUnwrapped(t *testing.T) {
	ensureFakeDrivers()

	argSets := []struct {
		name string
		args []any
	}{
		{"plain scalars", []any{int64(7), "plain"}},
		{"stmt checker sentinel", []any{"nvc", int64(1)}},
		{"conn checker sentinel", []any{"conn", int64(2)}},
		{"both sentinels", []any{"nvc", "conn"}},
		{"valuer", []any{valuerArg{s: "x"}, "plain"}},
		{"int needing default conversion", []any{5, true}},
		{"nil", []any{nil, "plain"}},
	}

	for _, kind := range []stmtKind{stmtPlain, stmtNVC, stmtCC, stmtNVCAndCC} {
		for _, connNVC := range []bool{false, true} {
			for _, set := range argSets {
				name := fmt.Sprintf("%s/conn=NamedValueChecker:%t/%s", kind, connNVC, set.name)
				t.Run(name, func(t *testing.T) {
					drvName := fakeDriverName(kind, connNVC)
					query := "INSERT INTO t VALUES (?, ?)"

					nativeDSN := "native:" + name
					nativeRec := registerRecorder(nativeDSN)
					native, err := sql.Open(drvName, nativeDSN)
					if err != nil {
						t.Fatalf("sql.Open: %v", err)
					}
					defer native.Close()
					_, nativeErr := native.ExecContext(context.Background(), query, set.args...)

					wrappedDSN := "wrapped:" + name
					wrappedRec := registerRecorder(wrappedDSN)
					wrapped, _, err := openInstrumented(drvName, "fake", wrappedDSN)
					if err != nil {
						t.Fatalf("openInstrumented: %v", err)
					}
					defer wrapped.Close()
					_, wrappedErr := wrapped.ExecContext(context.Background(), query, set.args...)

					if (nativeErr == nil) != (wrappedErr == nil) {
						t.Fatalf("error mismatch: native=%v wrapped=%v", nativeErr, wrappedErr)
					}
					if nativeErr != nil && nativeErr.Error() != wrappedErr.Error() {
						t.Fatalf("error text mismatch:\n native: %v\nwrapped: %v", nativeErr, wrappedErr)
					}

					got, want := wrappedRec.snapshot(), nativeRec.snapshot()
					if !reflect.DeepEqual(got, want) {
						t.Errorf("converted arguments differ from the unwrapped driver:\n native: %#v\nwrapped: %#v", want, got)
					}
				})
			}
		}
	}
}

// TestInstrumentedStmt_ColumnConverterExposureMatchesInner locks in the
// conditional: the wrapper must advertise driver.ColumnConverter exactly when
// the statement it wraps does. Advertising it always (or never) silently moves
// database/sql onto a different conversion route.
func TestInstrumentedStmt_ColumnConverterExposureMatchesInner(t *testing.T) {
	ensureFakeDrivers()

	for _, tc := range []struct {
		kind    stmtKind
		wantCC  bool
		wantNVC bool
	}{
		{stmtPlain, false, false},
		{stmtNVC, false, true},
		{stmtCC, true, false},
		{stmtNVCAndCC, true, true},
	} {
		t.Run(tc.kind.String(), func(t *testing.T) {
			dsn := "expose:" + tc.kind.String()
			registerRecorder(dsn)
			conn := &fakeConn{rec: lookupRecorder(dsn), kind: tc.kind}
			inner, err := conn.Prepare("INSERT INTO t VALUES (?)")
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}

			wrapper := newInstrumentedStmt(inner, &instrumentedConn{inner: conn, binding: &observerBinding{}}, "q")

			if _, ok := wrapper.(driver.ColumnConverter); ok != tc.wantCC { //nolint:staticcheck // asserting on the deprecated interface is the point
				t.Errorf("wrapper exposes ColumnConverter=%t, inner has %t", !tc.wantCC, tc.wantCC)
			}
			// The named-value checker is always exposed: the wrapper has to
			// run the statement-then-connection selection itself.
			if _, ok := wrapper.(driver.NamedValueChecker); !ok {
				t.Error("wrapper must always expose NamedValueChecker")
			}
			// Whether the inner statement has one decides what the wrapper's
			// checker does, not whether it exists.
			_, innerHasNVC := inner.(driver.NamedValueChecker)
			if innerHasNVC != tc.wantNVC {
				t.Fatalf("test setup: inner NamedValueChecker=%t want %t", innerHasNVC, tc.wantNVC)
			}
		})
	}
}
