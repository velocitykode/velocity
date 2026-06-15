package http_test

import (
	"testing"

	"github.com/velocitykode/velocity/router"
	velhttp "github.com/velocitykode/velocity/testing/http"
)

func newFragmentRouter() *router.VelocityRouterV2 {
	r := router.NewV2()
	r.Get("/fragment", func(c *router.Context) error {
		return c.JSON(200, map[string]any{
			"name":       "John",
			"age":        30,
			"deleted_at": nil,
			"user": map[string]any{
				"name": "Alice",
				"address": map[string]any{
					"city": "Portland",
				},
			},
			"tags": []any{"a", "b", "c"},
		})
	})
	return r
}

func TestAssertJSONFragment(t *testing.T) {
	tests := []struct {
		name    string
		subset  map[string]any
		wantErr bool
	}{
		{
			name:   "object subset",
			subset: map[string]any{"name": "John"},
		},
		{
			name:   "nested object",
			subset: map[string]any{"user": map[string]any{"address": map[string]any{"city": "Portland"}}},
		},
		{
			name:   "array subset",
			subset: map[string]any{"tags": []any{"a", "b"}},
		},
		{
			name:   "int vs float64 coercion",
			subset: map[string]any{"age": 30},
		},
		{
			name:   "null-valued subset",
			subset: map[string]any{"deleted_at": nil},
		},
		{
			name:    "null vs non-null mismatch",
			subset:  map[string]any{"name": nil},
			wantErr: true,
		},
		{
			name:    "scalar mismatch",
			subset:  map[string]any{"name": "Jane"},
			wantErr: true,
		},
		{
			name:    "missing key",
			subset:  map[string]any{"missing": 1},
			wantErr: true,
		},
		{
			name:    "nested mismatch",
			subset:  map[string]any{"user": map[string]any{"address": map[string]any{"city": "Seattle"}}},
			wantErr: true,
		},
		{
			name:    "array longer than response",
			subset:  map[string]any{"tags": []any{"a", "b", "c", "d"}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mt := &mockT{}
			client := velhttp.NewTestClient(mt, newFragmentRouter())
			got := client.Get("/fragment").AssertJSONFragment(tt.subset)
			if got == nil {
				t.Fatal("AssertJSONFragment returned nil, expected chainable response")
			}
			if tt.wantErr && len(mt.errors) == 0 {
				t.Errorf("expected fragment assertion to record an error, got none")
			}
			if !tt.wantErr && len(mt.errors) != 0 {
				t.Errorf("expected no errors, got %v", mt.errors)
			}
		})
	}
}
