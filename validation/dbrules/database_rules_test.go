package dbrules

import "testing"

func TestValidateIdentifier(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"simple", "users", false},
		{"underscore", "user_profiles", false},
		{"dotted table.column", "users.email", false},
		{"dotted schema.table", "public.users", false},
		{"dotted schema.table.column", "public.users.email", false},
		{"empty", "", true},
		{"leading digit", "1users", true},
		{"leading dot", ".users", true},
		{"trailing dot", "users.", true},
		{"double dot", "users..email", true},
		{"semicolon injection", "users; DROP TABLE", true},
		{"dash", "user-profiles", true},
		{"backtick", "users`", true},
		{"quote", "users\"", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateIdentifier(tt.id)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateIdentifier(%q): want err=%v, got %v", tt.id, tt.wantErr, err)
			}
		})
	}
}

func TestQuoteIdentifier(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		driver string
		want   string
	}{
		{"postgres simple", "users", "postgres", `"users"`},
		{"mysql simple", "users", "mysql", "`users`"},
		{"sqlite simple", "users", "sqlite", "`users`"},

		{"postgres schema.table", "public.users", "postgres", `"public"."users"`},
		{"mysql schema.table", "public.users", "mysql", "`public`.`users`"},
		{"sqlite schema.table", "public.users", "sqlite", "`public`.`users`"},

		{"postgres table.column", "users.email", "postgres", `"users"."email"`},
		{"mysql table.column", "users.email", "mysql", "`users`.`email`"},
		{"sqlite table.column", "users.email", "sqlite", "`users`.`email`"},

		{"postgres three-part", "public.users.email", "postgres", `"public"."users"."email"`},
		{"mysql three-part", "public.users.email", "mysql", "`public`.`users`.`email`"},

		{"unknown driver falls back to postgres quoter", "users", "mssql", `"users"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := quoteIdentifier(tt.input, tt.driver)
			if got != tt.want {
				t.Fatalf("quoteIdentifier(%q, %q): got %q want %q", tt.input, tt.driver, got, tt.want)
			}
		})
	}
}

func TestPlaceholder(t *testing.T) {
	tests := []struct {
		driver string
		n      int
		want   string
	}{
		{"postgres", 1, "$1"},
		{"postgres", 2, "$2"},
		{"postgres", 10, "$10"},
		{"mysql", 1, "?"},
		{"mysql", 5, "?"},
		{"sqlite", 1, "?"},
		{"unknown-driver", 1, "?"},
	}
	for _, tt := range tests {
		t.Run(tt.driver, func(t *testing.T) {
			if got := placeholder(tt.driver, tt.n); got != tt.want {
				t.Fatalf("placeholder(%q, %d) = %q, want %q", tt.driver, tt.n, got, tt.want)
			}
		})
	}
}
