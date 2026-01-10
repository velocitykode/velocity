package rules

import (
	"testing"
)

func TestSameRule(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		value   interface{}
		params  []string
		data    map[string]interface{}
		wantErr bool
	}{
		{
			name:    "returns nil when value is nil",
			field:   "password",
			value:   nil,
			params:  []string{"password_confirmation"},
			data:    map[string]interface{}{"password_confirmation": "secret"},
			wantErr: false,
		},
		{
			name:    "returns nil when fields match with string values",
			field:   "password",
			value:   "secret123",
			params:  []string{"password_confirmation"},
			data:    map[string]interface{}{"password_confirmation": "secret123"},
			wantErr: false,
		},
		{
			name:    "returns nil when fields match with int values",
			field:   "value1",
			value:   42,
			params:  []string{"value2"},
			data:    map[string]interface{}{"value2": 42},
			wantErr: false,
		},
		{
			name:    "returns nil when fields match with slice values using deep equality",
			field:   "items1",
			value:   []interface{}{"a", "b", "c"},
			params:  []string{"items2"},
			data:    map[string]interface{}{"items2": []interface{}{"a", "b", "c"}},
			wantErr: false,
		},
		{
			name:    "returns nil when fields match with map values using deep equality",
			field:   "config1",
			value:   map[string]interface{}{"key": "value"},
			params:  []string{"config2"},
			data:    map[string]interface{}{"config2": map[string]interface{}{"key": "value"}},
			wantErr: false,
		},
		{
			name:    "returns error when fields do not match",
			field:   "password",
			value:   "secret123",
			params:  []string{"password_confirmation"},
			data:    map[string]interface{}{"password_confirmation": "different"},
			wantErr: true,
		},
		{
			name:    "returns error when other field is missing",
			field:   "password",
			value:   "secret123",
			params:  []string{"password_confirmation"},
			data:    map[string]interface{}{},
			wantErr: true,
		},
		{
			name:    "returns error when other field is nil but value is not",
			field:   "password",
			value:   "secret123",
			params:  []string{"password_confirmation"},
			data:    map[string]interface{}{"password_confirmation": nil},
			wantErr: true,
		},
		{
			name:    "returns error when no params provided",
			field:   "password",
			value:   "secret123",
			params:  []string{},
			data:    map[string]interface{}{},
			wantErr: true,
		},
		{
			name:    "returns error when types differ even if values look similar",
			field:   "value",
			value:   42,
			params:  []string{"other"},
			data:    map[string]interface{}{"other": "42"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := SameRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("SameRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDifferentRule(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		value   interface{}
		params  []string
		data    map[string]interface{}
		wantErr bool
	}{
		{
			name:    "returns nil when value is nil",
			field:   "new_password",
			value:   nil,
			params:  []string{"old_password"},
			data:    map[string]interface{}{"old_password": "secret"},
			wantErr: false,
		},
		{
			name:    "returns nil when fields are different",
			field:   "new_password",
			value:   "newSecret",
			params:  []string{"old_password"},
			data:    map[string]interface{}{"old_password": "oldSecret"},
			wantErr: false,
		},
		{
			name:    "returns nil when other field is missing",
			field:   "new_password",
			value:   "newSecret",
			params:  []string{"old_password"},
			data:    map[string]interface{}{},
			wantErr: false,
		},
		{
			name:    "returns nil when types differ",
			field:   "value",
			value:   42,
			params:  []string{"other"},
			data:    map[string]interface{}{"other": "42"},
			wantErr: false,
		},
		{
			name:    "returns error when fields are the same",
			field:   "new_password",
			value:   "samePassword",
			params:  []string{"old_password"},
			data:    map[string]interface{}{"old_password": "samePassword"},
			wantErr: true,
		},
		{
			name:    "returns error when int fields are the same",
			field:   "value1",
			value:   42,
			params:  []string{"value2"},
			data:    map[string]interface{}{"value2": 42},
			wantErr: true,
		},
		{
			name:    "returns error when slice fields are deeply equal",
			field:   "items1",
			value:   []interface{}{"a", "b"},
			params:  []string{"items2"},
			data:    map[string]interface{}{"items2": []interface{}{"a", "b"}},
			wantErr: true,
		},
		{
			name:    "returns error when no params provided",
			field:   "value",
			value:   "test",
			params:  []string{},
			data:    map[string]interface{}{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := DifferentRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("DifferentRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestInRule(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		value   interface{}
		params  []string
		data    map[string]interface{}
		wantErr bool
	}{
		{
			name:    "returns nil when value is nil",
			field:   "status",
			value:   nil,
			params:  []string{"active", "pending", "closed"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when string value is in list",
			field:   "status",
			value:   "active",
			params:  []string{"active", "pending", "closed"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when string value is last in list",
			field:   "status",
			value:   "closed",
			params:  []string{"active", "pending", "closed"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when int value is converted and found in list",
			field:   "priority",
			value:   1,
			params:  []string{"1", "2", "3"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when float value is converted and found in list",
			field:   "score",
			value:   3.14,
			params:  []string{"1.5", "3.14", "2.71"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when string value is not in list",
			field:   "status",
			value:   "invalid",
			params:  []string{"active", "pending", "closed"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when int value is not in list",
			field:   "priority",
			value:   5,
			params:  []string{"1", "2", "3"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when no params provided",
			field:   "status",
			value:   "active",
			params:  []string{},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns nil when bool true is converted and found",
			field:   "flag",
			value:   true,
			params:  []string{"true", "false"},
			data:    nil,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := InRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("InRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNotInRule(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		value   interface{}
		params  []string
		data    map[string]interface{}
		wantErr bool
	}{
		{
			name:    "returns nil when value is nil",
			field:   "status",
			value:   nil,
			params:  []string{"banned", "deleted"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when string value is not in list",
			field:   "status",
			value:   "active",
			params:  []string{"banned", "deleted"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when int value converted is not in list",
			field:   "priority",
			value:   5,
			params:  []string{"1", "2", "3"},
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when string value is in list",
			field:   "status",
			value:   "banned",
			params:  []string{"banned", "deleted"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when int value converted is in list",
			field:   "priority",
			value:   2,
			params:  []string{"1", "2", "3"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when value is last in disallowed list",
			field:   "status",
			value:   "deleted",
			params:  []string{"banned", "deleted"},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when no params provided",
			field:   "status",
			value:   "active",
			params:  []string{},
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when bool false is converted and found",
			field:   "flag",
			value:   false,
			params:  []string{"false"},
			data:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NotInRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("NotInRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestConfirmedRule(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		value   interface{}
		params  []string
		data    map[string]interface{}
		wantErr bool
	}{
		{
			name:    "returns nil when value is nil",
			field:   "password",
			value:   nil,
			params:  nil,
			data:    map[string]interface{}{"password_confirmation": "secret"},
			wantErr: false,
		},
		{
			name:    "returns nil when confirmation matches",
			field:   "password",
			value:   "secret123",
			params:  nil,
			data:    map[string]interface{}{"password_confirmation": "secret123"},
			wantErr: false,
		},
		{
			name:    "returns nil when int confirmation matches",
			field:   "code",
			value:   1234,
			params:  nil,
			data:    map[string]interface{}{"code_confirmation": 1234},
			wantErr: false,
		},
		{
			name:    "returns error when confirmation does not match",
			field:   "password",
			value:   "secret123",
			params:  nil,
			data:    map[string]interface{}{"password_confirmation": "different"},
			wantErr: true,
		},
		{
			name:    "returns error when confirmation field is missing",
			field:   "password",
			value:   "secret123",
			params:  nil,
			data:    map[string]interface{}{},
			wantErr: true,
		},
		{
			name:    "returns error when confirmation field is nil but value is not",
			field:   "password",
			value:   "secret123",
			params:  nil,
			data:    map[string]interface{}{"password_confirmation": nil},
			wantErr: true,
		},
		{
			name:    "returns error when types differ",
			field:   "value",
			value:   42,
			params:  nil,
			data:    map[string]interface{}{"value_confirmation": "42"},
			wantErr: true,
		},
		{
			name:    "uses field name with _confirmation suffix",
			field:   "email",
			value:   "test@example.com",
			params:  nil,
			data:    map[string]interface{}{"email_confirmation": "test@example.com"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ConfirmedRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("ConfirmedRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAcceptedRule(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		value   interface{}
		params  []string
		data    map[string]interface{}
		wantErr bool
	}{
		{
			name:    "returns error when value is nil",
			field:   "terms",
			value:   nil,
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns nil when bool value is true",
			field:   "terms",
			value:   true,
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when bool value is false",
			field:   "terms",
			value:   false,
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns nil when string value is yes",
			field:   "terms",
			value:   "yes",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when string value is YES uppercase",
			field:   "terms",
			value:   "YES",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when string value is on",
			field:   "terms",
			value:   "on",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when string value is ON uppercase",
			field:   "terms",
			value:   "ON",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when string value is 1",
			field:   "terms",
			value:   "1",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when string value is true",
			field:   "terms",
			value:   "true",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns nil when string value is TRUE uppercase",
			field:   "terms",
			value:   "TRUE",
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when string value is no",
			field:   "terms",
			value:   "no",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when string value is off",
			field:   "terms",
			value:   "off",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when string value is 0",
			field:   "terms",
			value:   "0",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when string value is false",
			field:   "terms",
			value:   "false",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when string value is empty",
			field:   "terms",
			value:   "",
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns nil when int value is 1",
			field:   "terms",
			value:   1,
			params:  nil,
			data:    nil,
			wantErr: false,
		},
		{
			name:    "returns error when int value is 0",
			field:   "terms",
			value:   0,
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error when int value is not 1",
			field:   "terms",
			value:   2,
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for unsupported type",
			field:   "terms",
			value:   []string{"yes"},
			params:  nil,
			data:    nil,
			wantErr: true,
		},
		{
			name:    "returns error for float type",
			field:   "terms",
			value:   1.0,
			params:  nil,
			data:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := AcceptedRule(tt.field, tt.value, tt.params, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("AcceptedRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
