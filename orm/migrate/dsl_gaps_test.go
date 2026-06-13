package migrate

import (
	"strings"
	"testing"
)

// These tests cover the DSL types added to close TableBuilder gaps:
// TimestampTz/TimestampsTz, Binary, SmallInteger, BigID, DefaultRaw, Check.
// They exercise pure SQL generation (newTableBuilder.ToSQL, columnToSQL,
// NewColumnBuilder.ToSQL) across all three drivers without a live database.

func TestTableBuilder_TimestampTz(t *testing.T) {
	tests := []struct {
		driver      string
		wantType    string
		wantDefault string // expected non-null default fragment
	}{
		{"postgres", "TIMESTAMPTZ", "DEFAULT NOW()"},
		{"mysql", "TIMESTAMP", "DEFAULT CURRENT_TIMESTAMP"},
		{"sqlite", "DATETIME", "DEFAULT CURRENT_TIMESTAMP"},
	}
	for _, tt := range tests {
		t.Run(tt.driver, func(t *testing.T) {
			// Non-nullable: type + managed default.
			b := newTableBuilder("t", tt.driver)
			b.TimestampTz("seen_at")
			sql := b.ToSQL()
			if !strings.Contains(sql, tt.wantType) {
				t.Errorf("missing %q in:\n%s", tt.wantType, sql)
			}
			if !strings.Contains(sql, tt.wantDefault) {
				t.Errorf("missing %q in:\n%s", tt.wantDefault, sql)
			}

			// Nullable: no managed default, no NOT NULL.
			bn := newTableBuilder("t", tt.driver)
			bn.TimestampTz("seen_at").Nullable()
			nsql := bn.ToSQL()
			if strings.Contains(nsql, tt.wantDefault) {
				t.Errorf("nullable timestamptz should not carry %q:\n%s", tt.wantDefault, nsql)
			}
			if strings.Contains(nsql, "NOT NULL") {
				t.Errorf("nullable timestamptz should not be NOT NULL:\n%s", nsql)
			}
		})
	}
}

func TestTableBuilder_TimestampsTz(t *testing.T) {
	b := newTableBuilder("t", "postgres")
	b.TimestampsTz()
	sql := b.ToSQL()
	for _, want := range []string{
		`"created_at" TIMESTAMPTZ DEFAULT NOW()`,
		`"updated_at" TIMESTAMPTZ DEFAULT NOW()`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
}

func TestTableBuilder_Binary(t *testing.T) {
	tests := []struct {
		driver string
		want   string
	}{
		{"postgres", `"data" BYTEA`},
		{"mysql", "`data` LONGBLOB"},
		{"sqlite", "`data` BLOB"},
	}
	for _, tt := range tests {
		t.Run(tt.driver, func(t *testing.T) {
			b := newTableBuilder("t", tt.driver)
			b.Binary("data")
			if sql := b.ToSQL(); !strings.Contains(sql, tt.want) {
				t.Errorf("missing %q in:\n%s", tt.want, sql)
			}
		})
	}
}

func TestTableBuilder_SmallInteger(t *testing.T) {
	tests := []struct {
		driver string
		want   string
	}{
		{"postgres", `"n" SMALLINT`},
		{"mysql", "`n` SMALLINT"},
		{"sqlite", "`n` INTEGER"},
	}
	for _, tt := range tests {
		t.Run(tt.driver, func(t *testing.T) {
			b := newTableBuilder("t", tt.driver)
			b.SmallInteger("n")
			if sql := b.ToSQL(); !strings.Contains(sql, tt.want) {
				t.Errorf("missing %q in:\n%s", tt.want, sql)
			}
		})
	}
}

func TestTableBuilder_BigID(t *testing.T) {
	tests := []struct {
		driver string
		want   string
	}{
		{"postgres", `"id" BIGSERIAL PRIMARY KEY`},
		{"mysql", "`id` BIGINT AUTO_INCREMENT PRIMARY KEY"},
		{"sqlite", "`id` INTEGER PRIMARY KEY AUTOINCREMENT"},
	}
	for _, tt := range tests {
		t.Run(tt.driver, func(t *testing.T) {
			b := newTableBuilder("t", tt.driver)
			b.BigID()
			sql := b.ToSQL()
			if !strings.Contains(sql, tt.want) {
				t.Errorf("missing %q in:\n%s", tt.want, sql)
			}
			// The PK definition is self-contained; no stray NOT NULL/UNIQUE/DEFAULT.
			if strings.Contains(sql, tt.want+" NOT NULL") {
				t.Errorf("BigID PK should not carry NOT NULL:\n%s", sql)
			}
		})
	}
}

func TestTableBuilder_DefaultRaw(t *testing.T) {
	t.Run("uuid gen_random_uuid", func(t *testing.T) {
		b := newTableBuilder("t", "postgres")
		b.UUID("uid").DefaultRaw("gen_random_uuid()")
		sql := b.ToSQL()
		if !strings.Contains(sql, "DEFAULT gen_random_uuid()") {
			t.Errorf("raw default not emitted unquoted:\n%s", sql)
		}
		if strings.Contains(sql, "'gen_random_uuid()'") {
			t.Errorf("raw default must not be quoted:\n%s", sql)
		}
	})
	t.Run("jsonb empty array", func(t *testing.T) {
		b := newTableBuilder("t", "postgres")
		b.JSONB("tags").DefaultRaw("'[]'::jsonb")
		sql := b.ToSQL()
		if !strings.Contains(sql, "DEFAULT '[]'::jsonb") {
			t.Errorf("raw jsonb default missing:\n%s", sql)
		}
	})
	t.Run("literal Default still quotes", func(t *testing.T) {
		b := newTableBuilder("t", "postgres")
		b.String("name").Default("bob")
		sql := b.ToSQL()
		if !strings.Contains(sql, "DEFAULT 'bob'") {
			t.Errorf("literal default should be quoted:\n%s", sql)
		}
	})
	t.Run("raw default applied on nullable timestamptz", func(t *testing.T) {
		// Nullable: no managed default, so the explicit raw default is the only one.
		b := newTableBuilder("t", "postgres")
		b.TimestampTz("seen_at").Nullable().DefaultRaw("now()")
		sql := b.ToSQL()
		if !strings.Contains(sql, "DEFAULT now()") {
			t.Errorf("DefaultRaw should be applied on timestamptz:\n%s", sql)
		}
		if strings.Contains(sql, "NOW()") {
			t.Errorf("managed default must not appear alongside explicit one:\n%s", sql)
		}
	})
	t.Run("explicit default overrides managed on non-null timestamptz", func(t *testing.T) {
		b := newTableBuilder("t", "postgres")
		b.TimestampTz("created_at").DefaultRaw("now()")
		sql := b.ToSQL()
		if !strings.Contains(sql, "DEFAULT now()") {
			t.Errorf("explicit default missing:\n%s", sql)
		}
		if strings.Contains(sql, "NOW()") {
			t.Errorf("managed default must not fire when explicit set:\n%s", sql)
		}
		if !strings.Contains(sql, "NOT NULL") {
			t.Errorf("non-nullable column should keep NOT NULL:\n%s", sql)
		}
	})
}

func TestTableBuilder_Check(t *testing.T) {
	t.Run("single check, no composite pk", func(t *testing.T) {
		b := newTableBuilder("t", "postgres")
		b.ID()
		b.Integer("age")
		b.Check("age_pos", "age >= 0")
		sql := b.ToSQL()
		if !strings.Contains(sql, `CONSTRAINT "age_pos" CHECK (age >= 0)`) {
			t.Errorf("missing check constraint:\n%s", sql)
		}
		// Last clause has no trailing comma before the closing paren.
		if !strings.HasSuffix(sql, "CHECK (age >= 0)\n)") {
			t.Errorf("unexpected tail:\n%s", sql)
		}
		// The column preceding the check must be comma-terminated.
		if !strings.Contains(sql, `"age" INTEGER NOT NULL,`) {
			t.Errorf("column before check missing comma:\n%s", sql)
		}
	})

	t.Run("composite pk and check ordering", func(t *testing.T) {
		b := newTableBuilder("t", "postgres")
		b.Integer("a")
		b.Integer("b")
		b.PrimaryKey("a", "b")
		b.Check("b_pos", "b > 0")
		sql := b.ToSQL()
		// PK clause comes first and is comma-terminated; check is last.
		if !strings.Contains(sql, `PRIMARY KEY ("a", "b"),`) {
			t.Errorf("composite PK should precede check with comma:\n%s", sql)
		}
		if !strings.HasSuffix(sql, `CONSTRAINT "b_pos" CHECK (b > 0)`+"\n)") {
			t.Errorf("check should be last clause:\n%s", sql)
		}
	})

	t.Run("multiple checks", func(t *testing.T) {
		b := newTableBuilder("t", "sqlite")
		b.ID()
		b.Integer("x")
		b.Check("x_lo", "x > 0")
		b.Check("x_hi", "x < 100")
		sql := b.ToSQL()
		if !strings.Contains(sql, "CHECK (x > 0),") {
			t.Errorf("first check should be comma-terminated:\n%s", sql)
		}
		if !strings.HasSuffix(sql, "CHECK (x < 100)\n)") {
			t.Errorf("last check should not be comma-terminated:\n%s", sql)
		}
	})
}

// Composite-PK-only output must be byte-identical to the pre-refactor form on
// every driver, guarding the extraClauses() rewrite against regressions.
func TestTableBuilder_CompositePK_Unchanged(t *testing.T) {
	tests := []struct {
		driver string
		want   string
	}{
		{"postgres", "CREATE TABLE \"pivot\" (\n" +
			"  \"user_id\" INTEGER NOT NULL,\n" +
			"  \"role_id\" INTEGER NOT NULL,\n" +
			"  PRIMARY KEY (\"user_id\", \"role_id\")\n)"},
		{"mysql", "CREATE TABLE `pivot` (\n" +
			"  `user_id` INT NOT NULL,\n" +
			"  `role_id` INT NOT NULL,\n" +
			"  PRIMARY KEY (`user_id`, `role_id`)\n)"},
		{"sqlite", "CREATE TABLE `pivot` (\n" +
			"  `user_id` INTEGER NOT NULL,\n" +
			"  `role_id` INTEGER NOT NULL,\n" +
			"  PRIMARY KEY (`user_id`, `role_id`)\n)"},
	}
	for _, tt := range tests {
		t.Run(tt.driver, func(t *testing.T) {
			b := newTableBuilder("pivot", tt.driver)
			b.Integer("user_id")
			b.Integer("role_id")
			b.PrimaryKey("user_id", "role_id")
			if got := b.ToSQL(); got != tt.want {
				t.Errorf("composite PK SQL drifted:\ngot:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

// CHECK rendering must be correct on every driver (quoting differs).
func TestTableBuilder_Check_AllDrivers(t *testing.T) {
	for _, drv := range []string{"postgres", "mysql", "sqlite"} {
		t.Run(drv, func(t *testing.T) {
			b := newTableBuilder("t", drv)
			b.ID()
			b.Integer("age")
			b.Check("age_pos", "age >= 0")
			sql := b.ToSQL()
			want := "CONSTRAINT " + quoteIdentifier("age_pos", drv) + " CHECK (age >= 0)"
			if !strings.Contains(sql, want) {
				t.Errorf("[%s] missing %q in:\n%s", drv, want, sql)
			}
		})
	}
}

func TestColumnToSQL_NewTypes_AlterPath(t *testing.T) {
	tests := []struct {
		name   string
		col    Column
		driver string
		want   string
	}{
		{"binary pg", Column{Name: "data", Type: "binary", Nullable: true}, "postgres", `"data" BYTEA`},
		{"binary mysql", Column{Name: "data", Type: "binary", Nullable: true}, "mysql", "`data` LONGBLOB"},
		{"binary sqlite", Column{Name: "data", Type: "binary", Nullable: true}, "sqlite", "`data` BLOB"},
		{"smallint pg", Column{Name: "n", Type: "smallinteger", Nullable: true}, "postgres", `"n" SMALLINT`},
		{"smallint sqlite", Column{Name: "n", Type: "smallinteger", Nullable: true}, "sqlite", "`n` INTEGER"},
		{"timestamptz pg null", Column{Name: "ts", Type: "timestamptz", Nullable: true}, "postgres", `"ts" TIMESTAMPTZ`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := columnToSQL(tt.col, tt.driver); got != tt.want {
				t.Errorf("columnToSQL = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestColumnToSQL_TimestampTz_NotNull_NoDoubleDefault(t *testing.T) {
	got := columnToSQL(Column{Name: "ts", Type: "timestamptz", Nullable: false}, "postgres")
	want := `"ts" TIMESTAMPTZ DEFAULT NOW() NOT NULL`
	if got != want {
		t.Errorf("columnToSQL = %q, want %q", got, want)
	}
}

// TestColumnToSQL_AddColumnContext pins B31: Table() generates its ALTER TABLE
// ADD COLUMN column SQL via columnToSQL, which now renders under the add-column
// context. So SQLite emits a bare DATETIME NOT NULL (no managed CURRENT_TIMESTAMP
// default, which SQLite rejects on ADD COLUMN), while postgres/mysql keep their
// managed default. The fragment must also match the ColumnBuilder AddColumn path
// for the same spec, proving the two add paths stay consistent.
func TestColumnToSQL_AddColumnContext(t *testing.T) {
	tests := []struct {
		driver      string
		want        string
		wantManaged bool
	}{
		{"sqlite", "`ts` DATETIME NOT NULL", false},
		{"postgres", `"ts" TIMESTAMPTZ DEFAULT NOW() NOT NULL`, true},
		{"mysql", "`ts` TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL", true},
	}
	for _, tt := range tests {
		t.Run(tt.driver, func(t *testing.T) {
			got := columnToSQL(Column{Name: "ts", Type: "timestamptz", Nullable: false}, tt.driver)
			if got != tt.want {
				t.Errorf("columnToSQL = %q, want %q", got, tt.want)
			}
			if !tt.wantManaged && strings.Contains(got, "CURRENT_TIMESTAMP") {
				t.Errorf("add-column path must not carry a non-constant default: %q", got)
			}
			// Table()'s column fragment must equal the AddColumn fragment.
			builderSQL, err := NewColumnBuilder("ts", tt.driver).TimestampTz().ToSQL()
			if err != nil {
				t.Fatal(err)
			}
			if got != builderSQL {
				t.Errorf("Table() fragment %q != AddColumn fragment %q", got, builderSQL)
			}
		})
	}
}

func TestColumnBuilder_NewTypes(t *testing.T) {
	t.Run("binary nullable", func(t *testing.T) {
		sql, err := NewColumnBuilder("data", "postgres").Binary().Nullable().ToSQL()
		if err != nil {
			t.Fatal(err)
		}
		if sql != `"data" BYTEA` {
			t.Errorf("got %q", sql)
		}
	})
	t.Run("smallinteger", func(t *testing.T) {
		sql, err := NewColumnBuilder("n", "mysql").SmallInteger().ToSQL()
		if err != nil {
			t.Fatal(err)
		}
		if sql != "`n` SMALLINT NOT NULL" {
			t.Errorf("got %q", sql)
		}
	})
	t.Run("nullable timestamptz has no default", func(t *testing.T) {
		sql, err := NewColumnBuilder("ts", "postgres").TimestampTz().Nullable().ToSQL()
		if err != nil {
			t.Fatal(err)
		}
		if sql != `"ts" TIMESTAMPTZ` {
			t.Errorf("got %q", sql)
		}
	})
	t.Run("non-null timestamptz gets managed default on add (pg/mysql)", func(t *testing.T) {
		// AddColumn must backfill a non-nullable timestamp with the managed
		// default on engines whose ALTER TABLE ADD COLUMN accepts a volatile
		// default. SQLite cannot (see next case).
		tests := []struct {
			driver string
			want   string
		}{
			{"postgres", `"ts" TIMESTAMPTZ DEFAULT NOW() NOT NULL`},
			{"mysql", "`ts` TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL"},
		}
		for _, tt := range tests {
			sql, err := NewColumnBuilder("ts", tt.driver).TimestampTz().ToSQL()
			if err != nil {
				t.Fatalf("[%s] %v", tt.driver, err)
			}
			if sql != tt.want {
				t.Errorf("[%s] got %q, want %q", tt.driver, sql, tt.want)
			}
		}
	})
	t.Run("sqlite add timestamp has no volatile default", func(t *testing.T) {
		// SQLite ALTER TABLE ADD COLUMN cannot take a CURRENT_TIMESTAMP default,
		// so the managed default is not emitted on the SQLite add path. The bare
		// DATETIME NOT NULL works on empty tables and gets a clear native SQLite
		// error on populated ones (the same as any non-null column add).
		sql, err := NewColumnBuilder("ts", "sqlite").TimestampTz().ToSQL()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(sql, "CURRENT_TIMESTAMP") {
			t.Errorf("sqlite ADD COLUMN must not carry a non-constant default: %q", sql)
		}
		if sql != "`ts` DATETIME NOT NULL" {
			t.Errorf("got %q", sql)
		}
	})
	t.Run("sqlite add nullable timestamp ok", func(t *testing.T) {
		sql, err := NewColumnBuilder("ts", "sqlite").TimestampTz().Nullable().ToSQL()
		if err != nil {
			t.Fatal(err)
		}
		if sql != "`ts` DATETIME" {
			t.Errorf("got %q", sql)
		}
	})
	t.Run("explicit default on timestamptz add not doubled", func(t *testing.T) {
		sql, err := NewColumnBuilder("ts", "postgres").TimestampTz().DefaultRaw("now()").ToSQL()
		if err != nil {
			t.Fatal(err)
		}
		if sql != `"ts" TIMESTAMPTZ NOT NULL DEFAULT now()` {
			t.Errorf("got %q", sql)
		}
	})
	t.Run("default raw", func(t *testing.T) {
		sql, err := NewColumnBuilder("uid", "postgres").UUID().DefaultRaw("gen_random_uuid()").ToSQL()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(sql, "DEFAULT gen_random_uuid()") || strings.Contains(sql, "'gen_random_uuid()'") {
			t.Errorf("raw default not emitted unquoted: %q", sql)
		}
	})
}
