package validate

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCheck_FormData(t *testing.T) {
	form := url.Values{}
	form.Set("title", "Hi")
	form.Set("body", "Short")

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	errors := Check(r, Rules{
		"title": {"required", "min:3"},
		"body":  {"required", "min:10"},
	})

	if !errors.HasErrors() {
		t.Fatal("expected validation errors")
	}
	if errors.First("title") == "" {
		t.Error("expected error for title")
	}
	if errors.First("body") == "" {
		t.Error("expected error for body")
	}
}

func TestCheck_JSON(t *testing.T) {
	body := `{"email":"not-an-email","name":""}`
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(body)))
	r.Header.Set("Content-Type", "application/json")

	errors := Check(r, Rules{
		"name":  {"required"},
		"email": {"required", "email"},
	})

	if !errors.HasErrors() {
		t.Fatal("expected validation errors")
	}
	if errors.First("name") == "" {
		t.Error("expected error for name")
	}
	if errors.First("email") == "" {
		t.Error("expected error for email")
	}
}

func TestCheck_Valid(t *testing.T) {
	form := url.Values{}
	form.Set("title", "Hello World")
	form.Set("body", "This is a long enough body text")

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	errors := Check(r, Rules{
		"title": {"required", "min:3"},
		"body":  {"required", "min:10"},
	})

	if errors.HasErrors() {
		t.Fatalf("expected no errors, got: %v", errors.All())
	}
}

func TestCheck_CustomMessages(t *testing.T) {
	form := url.Values{}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	errors := Check(r, Rules{
		"title": {"required"},
	}, Messages{
		"title.required": "Please enter a title",
	})

	if !errors.HasErrors() {
		t.Fatal("expected errors")
	}
	if errors.First("title") != "Please enter a title" {
		t.Errorf("expected custom message, got: %s", errors.First("title"))
	}
}

func TestCheckData(t *testing.T) {
	data := map[string]interface{}{
		"name":  "Ali",
		"email": "bad",
	}

	errors := CheckData(data, Rules{
		"name":  {"required"},
		"email": {"required", "email"},
	})

	if !errors.HasErrors() {
		t.Fatal("expected errors")
	}
	if errors.First("name") != "" {
		t.Error("name should be valid")
	}
	if errors.First("email") == "" {
		t.Error("expected error for email")
	}
}

func TestErrors_All(t *testing.T) {
	e := &Errors{
		errors: map[string][]string{
			"name":  {"Name is required", "Name too short"},
			"email": {"Invalid email"},
		},
	}

	all := e.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 fields, got %d", len(all))
	}
	if all["name"] != "Name is required" {
		t.Errorf("expected first error for name, got: %s", all["name"])
	}
}

func TestErrors_Messages(t *testing.T) {
	e := &Errors{
		errors: map[string][]string{
			"name": {"err1", "err2"},
		},
	}

	msgs := e.Messages()
	if len(msgs["name"]) != 2 {
		t.Errorf("expected 2 messages for name, got %d", len(msgs["name"]))
	}
}

func TestErrors_Old(t *testing.T) {
	e := &Errors{
		input: map[string]interface{}{
			"name":                  "Ali",
			"email":                 "ali@test.com",
			"password":              "secret123",
			"password_confirmation": "secret123",
			"api_token":             "tok123",
			"client_secret":         "sec123",
		},
	}

	old := e.Old()
	if old["name"] != "Ali" {
		t.Error("name should be in old")
	}
	if old["email"] != "ali@test.com" {
		t.Error("email should be in old")
	}
	if _, ok := old["password"]; ok {
		t.Error("password should be stripped")
	}
	if _, ok := old["password_confirmation"]; ok {
		t.Error("password_confirmation should be stripped")
	}
	if _, ok := old["api_token"]; ok {
		t.Error("api_token should be stripped")
	}
	if _, ok := old["client_secret"]; ok {
		t.Error("client_secret should be stripped")
	}
}

func TestErrors_NoErrors(t *testing.T) {
	e := &Errors{}
	if e.HasErrors() {
		t.Error("should have no errors")
	}
	if e.First("anything") != "" {
		t.Error("First should return empty string")
	}
	if len(e.All()) != 0 {
		t.Error("All should be empty")
	}
}
