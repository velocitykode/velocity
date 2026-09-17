package chain

import (
	"context"
	"errors"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/orm"
)

type stubSeeder struct{ name string }

func (s *stubSeeder) Name() string                            { return s.name }
func (s *stubSeeder) Run(context.Context, *orm.Manager) error { return nil }

func TestSeeders_AddPreservesOrderAndLooksUpByName(t *testing.T) {
	r := NewSeeders()
	region, role, user := &stubSeeder{"region"}, &stubSeeder{"role"}, &stubSeeder{"user"}
	r.Add(region, role)
	r.Add(user)

	all := r.All()
	if len(all) != 3 {
		t.Fatalf("All() len = %d, want 3", len(all))
	}
	for i, want := range []string{"region", "role", "user"} {
		if all[i].Name() != want {
			t.Errorf("All()[%d] = %q, want %q", i, all[i].Name(), want)
		}
	}

	got, ok := r.Get("role")
	if !ok || got != role {
		t.Errorf("Get(role) = (%v, %v), want the registered seeder", got, ok)
	}
	if _, ok := r.Get("missing"); ok {
		t.Error("Get(missing) reported found")
	}
}

func TestSeeders_AllReturnsCopy(t *testing.T) {
	r := NewSeeders()
	r.Add(&stubSeeder{"a"})
	all := r.All()
	all[0] = &stubSeeder{"mutated"}
	if again := r.All(); again[0].Name() != "a" {
		t.Errorf("All() exposed internal slice: %q", again[0].Name())
	}
}

func TestSeeders_AddPanicsOnBadRegistration(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(r *Seeders)
		add     []interface{ Name() string }
		want    string
	}{
		{name: "nil", want: "cannot register nil seeder"},
		{name: "empty name", want: "seeder name cannot be empty"},
		{name: "duplicate", want: "duplicate seeder name: role"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewSeeders()
			defer func() {
				rec := recover()
				if rec == nil {
					t.Fatal("Add did not panic")
				}
				err, ok := rec.(error)
				if !ok {
					t.Fatalf("panic value %T is not an error", rec)
				}
				var regErr *contract.RegistrationError
				if !errors.As(err, &regErr) {
					t.Fatalf("panic error %v is not a *contract.RegistrationError", err)
				}
				if regErr.Package != "seeders" || regErr.Message != tc.want {
					t.Errorf("RegistrationError = %+v, want package seeders / message %q", regErr, tc.want)
				}
			}()
			switch tc.name {
			case "nil":
				r.Add(nil)
			case "empty name":
				r.Add(&stubSeeder{""})
			case "duplicate":
				r.Add(&stubSeeder{"role"})
				r.Add(&stubSeeder{"role"})
			}
		})
	}
}
