package vform

// Custom rules carry their own handler, so they must run wherever a rule set
// reaches. These tests drive one through the two adopter-facing entry points:
// vform.Form and ctx.Validate.

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/validation"
	"github.com/velocitykode/velocity/validation/dbrules"
)

// customRuleRan records that the handler executed, so a test cannot pass by
// the rule being silently skipped.
var customRuleRan bool

// evenLength is one rule value, reused wherever the rule applies.
var evenLength = validation.Custom("even_length", func(field string, value interface{}, params []string, data map[string]interface{}) error {
	customRuleRan = true
	s, _ := value.(string)
	if len(s)%2 != 0 {
		return fmt.Errorf("The %s field must have an even number of characters.", field)
	}
	return nil
})

// customRequest is a form request whose rules include a Custom rule.
type customRequest struct {
	Code string `json:"code"`
}

func (customRequest) Rules() validation.Rules {
	return validation.Rules{"code": {validation.Required(), evenLength}}
}

func TestCustomRule_ThroughVformForm(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantFail bool
	}{
		{name: "even length passes", body: `{"code":"abcd"}`, wantFail: false},
		{name: "odd length fails", body: `{"code":"abc"}`, wantFail: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			customRuleRan = false
			ctx, _ := jsonCtx(t, tc.body)

			req, err := Form[customRequest](ctx)
			if !customRuleRan {
				t.Fatal("the custom handler never ran")
			}

			if tc.wantFail {
				if !errors.Is(err, router.ErrValidationAborted) {
					t.Fatalf("error = %v, want router.ErrValidationAborted", err)
				}
				if req != nil {
					t.Error("no form should be returned on failure")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if req == nil || req.Code != "abcd" {
				t.Fatalf("form = %+v, want the bound code", req)
			}
		})
	}
}

func TestCustomRule_ThroughContextValidate(t *testing.T) {
	// Wire the router the way velocity.New does: ctx.Validate goes through
	// the DB-aware Check helper, which is also where a carried handler has
	// to survive.
	r := router.New()
	r.SetServices(&app.Services{})
	r.SetValidator(func(c *router.Context, rules contract.ValidationRuleSet, messages ...contract.ValidationMessages) error {
		result, err := dbrules.CheckWithDBW(c.Response, c.Request, rules, nil, messages...)
		if err != nil {
			return err
		}
		if !result.HasErrors() {
			return nil
		}
		return result.Err()
	})

	var validateErr error
	r.Post("/codes", func(c *router.Context) error {
		validateErr = c.Validate(validation.Rules{"code": {validation.Required(), evenLength}})
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})

	tests := []struct {
		name     string
		body     string
		wantFail bool
	}{
		{name: "even length passes", body: `{"code":"abcd"}`, wantFail: false},
		{name: "odd length fails", body: `{"code":"abc"}`, wantFail: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			customRuleRan = false
			validateErr = nil

			rec := httptest.NewRecorder()
			httpReq := httptest.NewRequest(http.MethodPost, "/codes", strings.NewReader(tc.body))
			httpReq.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(rec, httpReq)

			if !customRuleRan {
				t.Fatal("the custom handler never ran")
			}
			if failed := validateErr != nil; failed != tc.wantFail {
				t.Fatalf("failed = %v, want %v (err %v)", failed, tc.wantFail, validateErr)
			}
			if !tc.wantFail {
				return
			}

			var verr validation.ValidationErrors
			if !errors.As(validateErr, &verr) {
				t.Fatalf("error does not carry field errors: %T %v", validateErr, validateErr)
			}
			if got := verr.First("code"); got != "The code field must have an even number of characters." {
				t.Errorf("message = %q, want the custom rule's message", got)
			}
		})
	}
}
