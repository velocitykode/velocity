package migrate

import (
	"strings"
	"testing"
)

func TestTableBuilder_Vector_PostgresType(t *testing.T) {
	b := newTableBuilder("documents", "postgres")
	b.ID()
	b.Vector("embedding", 1536)
	sql := b.ToSQL()
	if !strings.Contains(sql, `"embedding" vector(1536)`) {
		t.Errorf("CREATE TABLE missing vector column type:\n%s", sql)
	}
	// Not nullable by default, so it must carry NOT NULL.
	if !strings.Contains(sql, `"embedding" vector(1536) NOT NULL`) {
		t.Errorf("expected NOT NULL on vector column:\n%s", sql)
	}
}

func TestTableBuilder_Vector_Nullable(t *testing.T) {
	b := newTableBuilder("documents", "postgres")
	b.Vector("embedding", 768).Nullable()
	sql := b.ToSQL()
	if strings.Contains(sql, "NOT NULL") {
		t.Errorf("nullable vector column should not be NOT NULL:\n%s", sql)
	}
	if !strings.Contains(sql, `"embedding" vector(768)`) {
		t.Errorf("missing vector(768):\n%s", sql)
	}
}

func TestColumnToSQL_VectorAlterPath(t *testing.T) {
	got := columnToSQL(Column{Name: "embedding", Type: "vector", Dimensions: 384, Nullable: true}, "postgres")
	want := `"embedding" vector(384)`
	if got != want {
		t.Errorf("columnToSQL = %q, want %q", got, want)
	}
}

func TestValidateVectorColumn(t *testing.T) {
	tests := []struct {
		name    string
		col     Column
		driver  string
		wantErr bool
	}{
		{"non-vector ignored", Column{Name: "x", Type: "string"}, "postgres", false},
		{"valid", Column{Name: "e", Type: "vector", Dimensions: 1536}, "postgres", false},
		{"zero dims", Column{Name: "e", Type: "vector", Dimensions: 0}, "postgres", true},
		{"negative dims", Column{Name: "e", Type: "vector", Dimensions: -1}, "postgres", true},
		{"over max dims", Column{Name: "e", Type: "vector", Dimensions: pgvectorMaxDimensions + 1}, "postgres", true},
		{"max dims ok", Column{Name: "e", Type: "vector", Dimensions: pgvectorMaxDimensions}, "postgres", false},
		{"non-postgres rejected", Column{Name: "e", Type: "vector", Dimensions: 3}, "mysql", true},
		{"sqlite rejected", Column{Name: "e", Type: "vector", Dimensions: 3}, "sqlite", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateVectorColumn(tt.col, tt.driver)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateVectorColumn(%+v, %q) err = %v, wantErr = %v", tt.col, tt.driver, err, tt.wantErr)
			}
		})
	}
}

func TestIndexBuilder_VectorHNSW(t *testing.T) {
	b := NewIndexBuilder("idx_documents_embedding_hnsw", "documents", "postgres")
	b.Columns("embedding").Using("hnsw").OperatorClass("vector_cosine_ops")
	sql, err := b.ToSQL()
	if err != nil {
		t.Fatalf("ToSQL() error = %v", err)
	}
	want := `CREATE INDEX "idx_documents_embedding_hnsw" ON "documents" USING hnsw ("embedding" vector_cosine_ops)`
	if sql != want {
		t.Errorf("SQL = %q\nwant %q", sql, want)
	}
}

func TestIndexBuilder_VectorIVFFlat(t *testing.T) {
	b := NewIndexBuilder("idx", "documents", "postgres")
	b.Columns("embedding").Using("ivfflat").OperatorClass("vector_l2_ops")
	sql, err := b.ToSQL()
	if err != nil {
		t.Fatalf("ToSQL() error = %v", err)
	}
	if !strings.Contains(sql, `USING ivfflat ("embedding" vector_l2_ops)`) {
		t.Errorf("unexpected SQL: %s", sql)
	}
}

func TestIndexBuilder_OperatorClass_Errors(t *testing.T) {
	t.Run("unknown opclass", func(t *testing.T) {
		b := NewIndexBuilder("idx", "documents", "postgres")
		b.Columns("embedding").Using("hnsw").OperatorClass("bogus_ops")
		if _, err := b.ToSQL(); err == nil {
			t.Fatal("expected error for unknown operator class")
		}
	})
	t.Run("multi-column with opclass", func(t *testing.T) {
		b := NewIndexBuilder("idx", "documents", "postgres")
		b.Columns("a", "b").OperatorClass("vector_cosine_ops")
		if _, err := b.ToSQL(); err == nil {
			t.Fatal("expected error: operator class requires a single column")
		}
	})
	t.Run("invalid method", func(t *testing.T) {
		b := NewIndexBuilder("idx", "documents", "postgres")
		b.Columns("embedding").Using("bogus")
		if _, err := b.ToSQL(); err == nil {
			t.Fatal("expected error for invalid index method")
		}
	})
}

func TestColumnBuilder_Vector_PostgresType(t *testing.T) {
	b := NewColumnBuilder("embedding", "postgres")
	b.Vector(384)
	sql, err := b.ToSQL()
	if err != nil {
		t.Fatalf("ToSQL() error = %v", err)
	}
	if !strings.Contains(sql, `"embedding" vector(384)`) {
		t.Errorf("ToSQL = %q, want vector(384)", sql)
	}
}

// Direct (non-Migrator) ColumnBuilder use must also fail closed: a vector
// column on a non-postgres driver or with an invalid dimension is an error,
// never a silent degrade to TEXT or vector(0).
func TestColumnBuilder_Vector_FailsClosed(t *testing.T) {
	t.Run("sqlite rejected", func(t *testing.T) {
		b := NewColumnBuilder("embedding", "sqlite")
		b.Vector(384)
		if _, err := b.ToSQL(); err == nil {
			t.Fatal("expected error: vector on sqlite")
		}
	})
	t.Run("zero dimensions rejected", func(t *testing.T) {
		b := NewColumnBuilder("embedding", "postgres")
		b.Vector(0)
		if _, err := b.ToSQL(); err == nil {
			t.Fatal("expected error: vector(0)")
		}
	})
	t.Run("Type(vector) without dims rejected", func(t *testing.T) {
		b := NewColumnBuilder("embedding", "postgres")
		b.Type("vector")
		if _, err := b.ToSQL(); err == nil {
			t.Fatal("expected error: Type(vector) with no dimensions")
		}
	})
}

func TestVectorIndex_NonPostgresRejected(t *testing.T) {
	for _, drv := range []string{"sqlite", "mysql"} {
		m := &Migrator{driver: drv}
		if err := m.VectorIndex("documents", "embedding", "hnsw", "vector_cosine_ops"); err == nil {
			t.Errorf("VectorIndex on %q: expected error, got nil", drv)
		}
	}
}

// Direct IndexBuilder use must also fail closed on non-postgres rather than
// silently dropping the vector method / operator class.
func TestIndexBuilder_VectorFeaturesRejectedNonPostgres(t *testing.T) {
	t.Run("hnsw on sqlite", func(t *testing.T) {
		b := NewIndexBuilder("idx", "documents", "sqlite")
		b.Columns("embedding").Using("hnsw")
		if _, err := b.ToSQL(); err == nil {
			t.Fatal("expected error: hnsw on sqlite")
		}
	})
	t.Run("opclass on mysql", func(t *testing.T) {
		b := NewIndexBuilder("idx", "documents", "mysql")
		b.Columns("embedding").OperatorClass("vector_cosine_ops")
		if _, err := b.ToSQL(); err == nil {
			t.Fatal("expected error: operator class on mysql")
		}
	})
}

// Without an operator class the column list still quotes/joins normally, so the
// new branch does not regress ordinary indexes.
func TestIndexBuilder_NoOpClass_Unchanged(t *testing.T) {
	b := NewIndexBuilder("idx", "documents", "postgres")
	b.Columns("a", "b")
	sql, err := b.ToSQL()
	if err != nil {
		t.Fatalf("ToSQL() error = %v", err)
	}
	if !strings.Contains(sql, `("a", "b")`) {
		t.Errorf("expected quoted column list, got: %s", sql)
	}
}
