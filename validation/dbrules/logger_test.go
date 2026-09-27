package dbrules

import (
	"context"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	logdrivers "github.com/velocitykode/velocity/log/drivers"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/trace"
	"github.com/velocitykode/velocity/validation"
)

// A unique or exists rule whose query fails writes one error line through
// the logger of the ORM manager it queried (the app logger in an app), and
// nothing through the standard library log, slog.Default or the fallback
// logger; the client-visible message stays generic.
func TestDatabaseRules_QueryFailureLogsThroughTheManagerLogger(t *testing.T) {
	for _, tc := range []struct {
		name string
		rule func(orm.Database) validation.RuleHandler
		msg  string
	}{
		{"unique", UniqueRule, "velocity/validation: unique rule query failed"},
		{"exists", ExistsRule, "velocity/validation: exists rule query failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdlib := fallbacklogtest.CaptureStdlib(t)
			fallback := fallbacklogtest.Capture(t)
			db := newSQLiteOrm(t)
			out := &fallbacklogtest.Output{}
			db.(*orm.Manager).SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))

			err := tc.rule(db)("email", "x@example.com", []string{"no_such_table", "email"}, nil)
			if err == nil || !strings.Contains(err.Error(), "Unable to validate email") {
				t.Fatalf("rule error = %v, want the generic message", err)
			}
			if strings.Contains(err.Error(), "no_such_table") {
				t.Errorf("client-visible error leaks the schema: %v", err)
			}

			if got := strings.Count(out.String(), "ERROR: "+tc.msg); got != 1 {
				t.Errorf("manager logger error lines = %d, want 1 (%q)", got, out.String())
			}
			if !strings.Contains(out.String(), "table=no_such_table") || !strings.Contains(out.String(), "error=") {
				t.Errorf("error line does not name the table and the error: %q", out.String())
			}
			if s := stdlib.String() + fallback.String(); s != "" {
				t.Errorf("stdlib / slog.Default / fallback got %q, want nothing", s)
			}
		})
	}
}

// A database without a logger of its own sends the line to the fallback
// logger.
func TestDatabaseRules_QueryFailureWithoutLoggerUsesTheFallback(t *testing.T) {
	stdlib := fallbacklogtest.CaptureStdlib(t)
	fallback := fallbacklogtest.Capture(t)
	db := newSQLiteOrm(t)

	_ = UniqueRule(db)("email", "x@example.com", []string{"no_such_table", "email"}, nil)

	if got := fallback.Count("ERROR", "velocity/validation: unique rule query failed"); got != 1 {
		t.Errorf("fallback error lines = %d, want 1 (%q)", got, fallback.String())
	}
	if s := stdlib.String(); s != "" {
		t.Errorf("stdlib / slog.Default got %q, want nothing", s)
	}
}

// The failed-query line carries the request, trace and span ids of the ctx
// the rule runs under (UniqueRuleCtx / ExistsRuleCtx, which the request
// validators use).
func TestDatabaseRules_QueryFailureCarriesTheRequestIDs(t *testing.T) {
	for _, tc := range []struct {
		name string
		rule func(context.Context, orm.Database) validation.RuleHandler
	}{
		{"unique", UniqueRuleCtx},
		{"exists", ExistsRuleCtx},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newSQLiteOrm(t)
			out := &fallbacklogtest.Output{}
			db.(*orm.Manager).SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
			ctx := trace.WithTrace(trace.WithRequestID(context.Background(), "req-rule-1"), "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7")

			_ = tc.rule(ctx, db)("email", "x@example.com", []string{"no_such_table", "email"}, nil)

			if want := "request_id=req-rule-1 trace_id=4bf92f3577b34da6a3ce929d0e0e4736 span_id=00f067aa0ba902b7"; !strings.Contains(out.String(), want) {
				t.Errorf("line %q does not carry %q", out.String(), want)
			}
		})
	}
}

// requestLogger's With binds pairs after the request fields, and a nil ctx
// binds none.
func TestRequestLogger_WithAndNilContext(t *testing.T) {
	out := &fallbacklogtest.Output{}
	base := logdrivers.NewConsoleLoggerTo(out, 0)
	ctx := trace.WithRequestID(context.Background(), "req-rule-2")
	requestLogger{ctx: ctx, logger: base}.With("rule", "unique").Error("bound")
	requestLogger{ctx: nil, logger: base}.Error("no ctx")
	lines := out.Lines()
	if len(lines) != 2 {
		t.Fatalf("lines = %q, want 2", lines)
	}
	if !strings.Contains(lines[0], "request_id=req-rule-2 rule=unique") {
		t.Errorf("line %q, want request_id then rule", lines[0])
	}
	if strings.Contains(lines[1], "request_id") {
		t.Errorf("line %q carries a request id without a ctx", lines[1])
	}
}
