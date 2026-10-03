package dbrules

import (
	"context"
	"errors"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/validation"
)

// TestDBRulesWithoutDatabase_ReportServiceNotConfigured pins the absence
// path: with no database (nil or typed nil), a rule set naming a DB rule is
// a configuration error wrapping both validation.ErrInvalidRule and the
// missing-service error naming "database", never a field failure; a DB rule
// built without a database reports the same missing-service error when it
// runs.
func TestDBRulesWithoutDatabase_ReportServiceNotConfigured(t *testing.T) {
	rules := validation.Rules{"email": {validation.Required(), validation.Unique("users", "email")}}
	data := map[string]interface{}{"email": "a@b.example"}
	for _, tc := range []struct {
		name string
		db   orm.Database
	}{
		{"nil", nil},
		{"typed nil", (*orm.Manager)(nil)},
	} {
		_, err := CheckDataWithDB(data, rules, tc.db)
		var snc *contract.ServiceNotConfiguredError
		if !errors.Is(err, validation.ErrInvalidRule) || !errors.As(err, &snc) || snc.Service != "database" {
			t.Errorf("%s: CheckDataWithDB = %v, want ErrInvalidRule and a ServiceNotConfiguredError naming database", tc.name, err)
		}
		var verr validation.ValidationErrors
		if errors.As(err, &verr) {
			t.Errorf("%s: a missing database surfaced as field errors", tc.name)
		}

		for name, h := range map[string]validation.RuleHandler{
			"unique": UniqueRuleCtx(context.Background(), tc.db),
			"exists": ExistsRuleCtx(context.Background(), tc.db),
		} {
			herr := h("email", "a@b.example", []string{"users", "email"}, data)
			if !errors.As(herr, &snc) || snc.Service != "database" {
				t.Errorf("%s: %s handler = %v, want a ServiceNotConfiguredError naming database", tc.name, name, herr)
			}
		}
	}
}
