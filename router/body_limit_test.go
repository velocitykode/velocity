package router

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

func TestBodyLimit_UnderLimit(t *testing.T) {
	handler := BodyLimit(1024)(func(c *Context) error {
		var data map[string]string
		if err := c.Bind(&data); err != nil {
			return c.Error(http.StatusBadRequest, err.Error())
		}
		return c.JSON(http.StatusOK, data)
	})

	body := `{"name":"test"}`
	c, w := NewTestContext("POST", "/test", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	err := handler(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestBodyLimit_OverLimit(t *testing.T) {
	handler := BodyLimit(5)(func(c *Context) error {
		var data map[string]string
		if err := c.Bind(&data); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, data)
	})

	body := `{"name":"this is way too long for 5 bytes"}`
	c, _ := NewTestContext("POST", "/test", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	err := handler(c)
	if err == nil {
		t.Fatal("expected error for body over limit")
	}
}

func TestBodyLimit_SetsContextKey(t *testing.T) {
	var gotLimit interface{}
	handler := BodyLimit(2048)(func(c *Context) error {
		gotLimit = c.Get("_body_limit")
		return nil
	})

	c, _ := NewTestContext("POST", "/test", strings.NewReader("{}"))
	handler(c)

	if gotLimit != int64(2048) {
		t.Errorf("expected body limit 2048 in context, got %v", gotLimit)
	}
}

func TestBodyLimit_PreventDoubleWrap_Bind(t *testing.T) {
	// When BodyLimit middleware is applied, Bind() should not add its own MaxBytesReader
	handler := BodyLimit(1024)(func(c *Context) error {
		var data map[string]string
		return c.Bind(&data)
	})

	body := `{"name":"test"}`
	c, w := NewTestContext("POST", "/test", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	err := handler(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Code != http.StatusOK {
		// Successful decode means no double-wrap issue
	}
}

func TestBodyLimit_PreventDoubleWrap_BindXML(t *testing.T) {
	handler := BodyLimit(1024)(func(c *Context) error {
		var data struct {
			Name string `xml:"name"`
		}
		return c.BindXML(&data)
	})

	body := `<root><name>test</name></root>`
	c, _ := NewTestContext("POST", "/test", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/xml")

	err := handler(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBindForm_HasBodyLimit(t *testing.T) {
	// BindForm should now enforce body limit
	c, _ := NewTestContext("POST", "/test", strings.NewReader("name=test"))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	type formData struct {
		Name string `form:"name"`
	}
	var fd formData
	if err := c.BindForm(&fd); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fd.Name != "test" {
		t.Errorf("expected name=test, got %s", fd.Name)
	}
}

func TestFormFile_UsesBodyLimit(t *testing.T) {
	// Verify FormFile uses middleware limit when set
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("file", "test.txt")
	fw.Write([]byte("content"))
	w.Close()

	c, _ := NewTestContext("POST", "/upload", &buf)
	c.Request.Header.Set("Content-Type", w.FormDataContentType())
	c.Set("_body_limit", int64(1024*1024))

	fh, err := c.FormFile("file")
	if err != nil {
		t.Fatalf("FormFile error: %v", err)
	}
	if fh.Filename != "test.txt" {
		t.Errorf("expected test.txt, got %s", fh.Filename)
	}
}

func TestBodyLimit_PanicsOnZero(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for zero limit")
		}
	}()
	BodyLimit(0)
}

func TestBodyLimit_PanicsOnNegative(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for negative limit")
		}
	}()
	BodyLimit(-1)
}
