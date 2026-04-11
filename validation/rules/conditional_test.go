package rules

import (
	"testing"
)

func TestRequiredIfRule(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		value   interface{}
		params  []string
		data    map[string]interface{}
		wantErr bool
	}{
		{
			name:    "passes when condition not met and value is nil",
			field:   "phone",
			value:   nil,
			params:  []string{"contact_method", "phone"},
			data:    map[string]interface{}{"contact_method": "email"},
			wantErr: false,
		},
		{
			name:    "passes when condition met and value is present",
			field:   "phone",
			value:   "555-1234",
			params:  []string{"contact_method", "phone"},
			data:    map[string]interface{}{"contact_method": "phone"},
			wantErr: false,
		},
		{
			name:    "fails when condition met and value is nil",
			field:   "phone",
			value:   nil,
			params:  []string{"contact_method", "phone"},
			data:    map[string]interface{}{"contact_method": "phone"},
			wantErr: true,
		},
		{
			name:    "fails when condition met and value is empty string",
			field:   "phone",
			value:   "",
			params:  []string{"contact_method", "phone"},
			data:    map[string]interface{}{"contact_method": "phone"},
			wantErr: true,
		},
		{
			name:    "fails when condition met and value is empty slice",
			field:   "tags",
			value:   []interface{}{},
			params:  []string{"type", "article"},
			data:    map[string]interface{}{"type": "article"},
			wantErr: true,
		},
		{
			name:    "passes when other field is absent",
			field:   "phone",
			value:   nil,
			params:  []string{"contact_method", "phone"},
			data:    map[string]interface{}{},
			wantErr: false,
		},
		{
			name:    "compares integer other field value as string",
			field:   "reason",
			value:   nil,
			params:  []string{"status", "1"},
			data:    map[string]interface{}{"status": 1},
			wantErr: true,
		},
		{
			name:    "compares boolean other field value as string",
			field:   "details",
			value:   nil,
			params:  []string{"active", "true"},
			data:    map[string]interface{}{"active": true},
			wantErr: true,
		},
		{
			name:    "fails when not enough params",
			field:   "phone",
			value:   "555",
			params:  []string{"contact_method"},
			data:    map[string]interface{}{},
			wantErr: true,
		},
		{
			name:    "fails when no params",
			field:   "phone",
			value:   "555",
			params:  []string{},
			data:    map[string]interface{}{},
			wantErr: true,
		},
		{
			name:    "passes with non-empty string slice value when condition met",
			field:   "tags",
			value:   []string{"go"},
			params:  []string{"type", "article"},
			data:    map[string]interface{}{"type": "article"},
			wantErr: false,
		},
		{
			name:    "fails with empty string slice value when condition met",
			field:   "tags",
			value:   []string{},
			params:  []string{"type", "article"},
			data:    map[string]interface{}{"type": "article"},
			wantErr: true,
		},
		{
			name:    "fails with empty map value when condition met",
			field:   "metadata",
			value:   map[string]interface{}{},
			params:  []string{"type", "article"},
			data:    map[string]interface{}{"type": "article"},
			wantErr: true,
		},
		{
			name:    "passes with non-empty map value when condition met",
			field:   "metadata",
			value:   map[string]interface{}{"key": "val"},
			params:  []string{"type", "article"},
			data:    map[string]interface{}{"type": "article"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := RequiredIfRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("RequiredIfRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRequiredUnlessRule(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		value   interface{}
		params  []string
		data    map[string]interface{}
		wantErr bool
	}{
		{
			name:    "passes when other field equals exempt value",
			field:   "reason",
			value:   nil,
			params:  []string{"status", "active"},
			data:    map[string]interface{}{"status": "active"},
			wantErr: false,
		},
		{
			name:    "fails when other field does not equal exempt value and value is nil",
			field:   "reason",
			value:   nil,
			params:  []string{"status", "active"},
			data:    map[string]interface{}{"status": "inactive"},
			wantErr: true,
		},
		{
			name:    "fails when other field does not equal exempt value and value is empty",
			field:   "reason",
			value:   "",
			params:  []string{"status", "active"},
			data:    map[string]interface{}{"status": "inactive"},
			wantErr: true,
		},
		{
			name:    "passes when other field does not equal exempt value and value is present",
			field:   "reason",
			value:   "because reasons",
			params:  []string{"status", "active"},
			data:    map[string]interface{}{"status": "inactive"},
			wantErr: false,
		},
		{
			name:    "fails when other field is absent and value is nil",
			field:   "reason",
			value:   nil,
			params:  []string{"status", "active"},
			data:    map[string]interface{}{},
			wantErr: true,
		},
		{
			name:    "passes when exempt value matches and value is empty",
			field:   "reason",
			value:   "",
			params:  []string{"status", "active"},
			data:    map[string]interface{}{"status": "active"},
			wantErr: false,
		},
		{
			name:    "compares integer other field value as string",
			field:   "reason",
			value:   nil,
			params:  []string{"status", "0"},
			data:    map[string]interface{}{"status": 0},
			wantErr: false,
		},
		{
			name:    "fails when not enough params",
			field:   "reason",
			value:   "test",
			params:  []string{"status"},
			data:    map[string]interface{}{},
			wantErr: true,
		},
		{
			name:    "fails when no params",
			field:   "reason",
			value:   "test",
			params:  []string{},
			data:    map[string]interface{}{},
			wantErr: true,
		},
		{
			name:    "fails with empty slice when condition met",
			field:   "items",
			value:   []interface{}{},
			params:  []string{"type", "standard"},
			data:    map[string]interface{}{"type": "premium"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := RequiredUnlessRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("RequiredUnlessRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRequiredWithRule(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		value   interface{}
		params  []string
		data    map[string]interface{}
		wantErr bool
	}{
		{
			name:    "passes when other field is absent",
			field:   "city",
			value:   nil,
			params:  []string{"address"},
			data:    map[string]interface{}{},
			wantErr: false,
		},
		{
			name:    "passes when other field is present and value is present",
			field:   "city",
			value:   "New York",
			params:  []string{"address"},
			data:    map[string]interface{}{"address": "123 Main St"},
			wantErr: false,
		},
		{
			name:    "fails when other field is present and value is nil",
			field:   "city",
			value:   nil,
			params:  []string{"address"},
			data:    map[string]interface{}{"address": "123 Main St"},
			wantErr: true,
		},
		{
			name:    "fails when other field is present and value is empty string",
			field:   "city",
			value:   "",
			params:  []string{"address"},
			data:    map[string]interface{}{"address": "123 Main St"},
			wantErr: true,
		},
		{
			name:    "fails when other field is present with nil value and this value is nil",
			field:   "city",
			value:   nil,
			params:  []string{"address"},
			data:    map[string]interface{}{"address": nil},
			wantErr: true,
		},
		{
			name:    "passes when other field is present with nil value and this value is present",
			field:   "city",
			value:   "New York",
			params:  []string{"address"},
			data:    map[string]interface{}{"address": nil},
			wantErr: false,
		},
		{
			name:    "fails when no params",
			field:   "city",
			value:   "test",
			params:  []string{},
			data:    map[string]interface{}{},
			wantErr: true,
		},
		{
			name:    "fails with empty slice when other field is present",
			field:   "tags",
			value:   []interface{}{},
			params:  []string{"title"},
			data:    map[string]interface{}{"title": "Hello"},
			wantErr: true,
		},
		{
			name:    "passes with non-empty slice when other field is present",
			field:   "tags",
			value:   []string{"go"},
			params:  []string{"title"},
			data:    map[string]interface{}{"title": "Hello"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := RequiredWithRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("RequiredWithRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRequiredWithoutRule(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		value   interface{}
		params  []string
		data    map[string]interface{}
		wantErr bool
	}{
		{
			name:    "passes when other field is present",
			field:   "phone",
			value:   nil,
			params:  []string{"email"},
			data:    map[string]interface{}{"email": "test@example.com"},
			wantErr: false,
		},
		{
			name:    "passes when other field is absent and value is present",
			field:   "phone",
			value:   "555-1234",
			params:  []string{"email"},
			data:    map[string]interface{}{},
			wantErr: false,
		},
		{
			name:    "fails when other field is absent and value is nil",
			field:   "phone",
			value:   nil,
			params:  []string{"email"},
			data:    map[string]interface{}{},
			wantErr: true,
		},
		{
			name:    "fails when other field is absent and value is empty string",
			field:   "phone",
			value:   "",
			params:  []string{"email"},
			data:    map[string]interface{}{},
			wantErr: true,
		},
		{
			name:    "passes when other field is present with empty value and this value is nil",
			field:   "phone",
			value:   nil,
			params:  []string{"email"},
			data:    map[string]interface{}{"email": ""},
			wantErr: false,
		},
		{
			name:    "fails when no params",
			field:   "phone",
			value:   "test",
			params:  []string{},
			data:    map[string]interface{}{},
			wantErr: true,
		},
		{
			name:    "fails with empty slice when other field is absent",
			field:   "tags",
			value:   []interface{}{},
			params:  []string{"category"},
			data:    map[string]interface{}{},
			wantErr: true,
		},
		{
			name:    "passes with non-empty slice when other field is absent",
			field:   "tags",
			value:   []string{"go", "web"},
			params:  []string{"category"},
			data:    map[string]interface{}{},
			wantErr: false,
		},
		{
			name:    "fails with empty string slice when other field is absent",
			field:   "tags",
			value:   []string{},
			params:  []string{"category"},
			data:    map[string]interface{}{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := RequiredWithoutRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("RequiredWithoutRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
