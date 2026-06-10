package orm

import (
	"testing"

	"github.com/velocitykode/velocity/orm/drivers"
)

// grammarOnlyDriver satisfies drivers.Driver for buildJoinOn, which only
// needs Grammar(). Every other method panics via the embedded nil Driver.
type grammarOnlyDriver struct {
	drivers.Driver
	grammar drivers.QueryGrammar
}

func (d *grammarOnlyDriver) Grammar() drivers.QueryGrammar { return d.grammar }

// TestJoin_QuotesQualifiedIdentifiersPerSegment asserts buildJoinOn quotes
// each dot segment separately: users.id must compile to `users`.`id` (or
// "users"."id" on postgres), not one quoted `users.id` identifier.
func TestJoin_QuotesQualifiedIdentifiersPerSegment(t *testing.T) {
	tests := []struct {
		name    string
		grammar drivers.QueryGrammar
		wantOn  string
	}{
		{"sqlite", &drivers.SQLiteGrammar{}, "`users`.`id` = `posts`.`user_id`"},
		{"mysql", &drivers.MySQLGrammar{}, "`users`.`id` = `posts`.`user_id`"},
		{"postgres", &drivers.PostgresGrammar{}, `"users"."id" = "posts"."user_id"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &Query[struct{}]{driver: &grammarOnlyDriver{grammar: tt.grammar}}
			q.Join("posts", "users.id", "=", "posts.user_id")
			if err := q.Err(); err != nil {
				t.Fatalf("Join returned error: %v", err)
			}
			if len(q.joins) != 1 {
				t.Fatalf("joins = %d, want 1", len(q.joins))
			}
			if got := q.joins[0].On; got != tt.wantOn {
				t.Errorf("Join ON = %q, want %q", got, tt.wantOn)
			}
		})
	}
}
