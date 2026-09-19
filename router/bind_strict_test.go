package router

import (
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func bindTestContext(body, contentType string) *Context {
	req := httptest.NewRequest("POST", "/test", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return NewContext(httptest.NewRecorder(), req)
}

func TestContext_Bind_SingleValue(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}

	tests := []struct {
		name    string
		body    string
		wantErr error
		want    string
	}{
		{"exact object", `{"name":"a"}`, nil, "a"},
		{"surrounding whitespace", " \r\n\t{\"name\":\"a\"} \r\n\t", nil, "a"},
		{"two concatenated objects", `{"name":"a"}{"name":"b"}`, ErrBindExtraData, ""},
		{"two objects newline separated", "{\"name\":\"a\"}\n{\"name\":\"b\"}", ErrBindExtraData, ""},
		{"trailing junk", `{"name":"a"} garbage`, ErrBindExtraData, ""},
		{"trailing closing bracket", `{"name":"a"}]`, ErrBindExtraData, ""},
		{"trailing closing brace", `{"name":"a"}}`, ErrBindExtraData, ""},
		{"trailing scalar", `{"name":"a"} 1`, ErrBindExtraData, ""},
		{"empty body", ``, io.EOF, ""},
		{"whitespace only body", "  \n ", io.EOF, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := bindTestContext(tt.body, "application/json")
			var p payload
			err := c.Bind(&p)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Bind(%q) error = %v, want %v", tt.body, err, tt.wantErr)
			}
			if tt.wantErr == nil && p.Name != tt.want {
				t.Errorf("Name = %q, want %q", p.Name, tt.want)
			}
		})
	}
}

func TestContext_Bind_MalformedStaysSyntaxError(t *testing.T) {
	c := bindTestContext(`{"name":`, "application/json")
	var p map[string]any
	err := c.Bind(&p)
	if err == nil || errors.Is(err, ErrBindExtraData) {
		t.Fatalf("expected a decode error distinct from ErrBindExtraData, got %v", err)
	}
}

func TestContext_Bind_TrailingOverLimitReportsBodyLimit(t *testing.T) {
	body := `{"name":"a"}` + strings.Repeat(" ", 64)
	req := httptest.NewRequest("POST", "/test", strings.NewReader(body))
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	// Simulate the BodyLimit middleware: it installs the reader and marks
	// the context so Bind does not wrap again.
	c.Request.Body = http.MaxBytesReader(w, c.Request.Body, 20)
	c.Set(bodyLimitKey, true)

	var p map[string]any
	err := c.Bind(&p)
	var tooLarge *http.MaxBytesError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("expected *http.MaxBytesError, got %v", err)
	}
}

func TestContext_BindXML_SingleDocument(t *testing.T) {
	type item struct {
		XMLName xml.Name `xml:"item"`
		Name    string   `xml:"name"`
	}
	const doc = `<item><name>a</name></item>`

	tests := []struct {
		name    string
		body    string
		wantErr error
	}{
		{"exact document", doc, nil},
		{"surrounding whitespace", "\n  " + doc + "\n  ", nil},
		{"xml declaration", `<?xml version="1.0"?>` + "\n" + doc, nil},
		{"doctype before root", `<!DOCTYPE item>` + doc, nil},
		{"comments around root", `<!-- a -->` + doc + `<!-- b -->`, nil},
		{"trailing processing instruction", doc + `<?pi x?>`, nil},
		{"leading BOM", "\xEF\xBB\xBF" + doc, nil},
		{"leading BOM with declaration", "\xEF\xBB\xBF" + `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + doc, nil},
		{"leading BOM then whitespace", "\xEF\xBB\xBF\n" + doc, nil},
		{"double BOM", "\xEF\xBB\xBF\xEF\xBB\xBF" + doc, ErrBindExtraData},
		{"BOM after whitespace", "\n\xEF\xBB\xBF" + doc, ErrBindExtraData},
		{"BOM after declaration", `<?xml version="1.0"?>` + "\xEF\xBB\xBF" + doc, ErrBindExtraData},
		{"trailing BOM", doc + "\xEF\xBB\xBF", ErrBindExtraData},
		{"BOM only body", "\xEF\xBB\xBF", io.EOF},
		{"two documents", doc + doc, ErrBindExtraData},
		{"trailing junk", doc + ` garbage`, ErrBindExtraData},
		{"trailing stray end tag", doc + `</item>`, ErrBindExtraData},
		{"trailing doctype", doc + `<!DOCTYPE item>`, ErrBindExtraData},
		{"leading junk", `garbage` + doc, ErrBindExtraData},
		{"empty body", ``, io.EOF},
		{"whitespace only body", " \n ", io.EOF},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := bindTestContext(tt.body, "application/xml")
			var it item
			err := c.BindXML(&it)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("BindXML(%q) error = %v, want %v", tt.body, err, tt.wantErr)
			}
			if tt.wantErr == nil && it.Name != "a" {
				t.Errorf("Name = %q, want a", it.Name)
			}
		})
	}
}

func TestContext_BindXML_MalformedStaysSyntaxError(t *testing.T) {
	c := bindTestContext(`<item><name>a</item>`, "application/xml")
	var it struct {
		Name string `xml:"name"`
	}
	err := c.BindXML(&it)
	if err == nil || errors.Is(err, ErrBindExtraData) {
		t.Fatalf("expected a decode error distinct from ErrBindExtraData, got %v", err)
	}
}

func TestContext_BindAuto_RejectsExtraData(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{"json", "application/json", `{"name":"a"}{"name":"b"}`},
		{"fallback json", "", `{"name":"a"} garbage`},
		{"xml", "application/xml", `<item><name>a</name></item><item/>`},
		{"text xml", "text/xml", `<item><name>a</name></item> garbage`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := bindTestContext(tt.body, tt.contentType)
			var v struct {
				Name string `json:"name" xml:"name"`
			}
			if err := c.BindAuto(&v); !errors.Is(err, ErrBindExtraData) {
				t.Fatalf("BindAuto error = %v, want ErrBindExtraData", err)
			}
		})
	}
}

func TestContext_BindValid_RejectsExtraData(t *testing.T) {
	c := bindTestContext(`{"name":"a"}{"name":"b"}`, "application/json")
	var v struct {
		Name string `json:"name"`
	}
	if err := c.BindValid(&v); !errors.Is(err, ErrBindExtraData) {
		t.Fatalf("BindValid error = %v, want ErrBindExtraData", err)
	}
}

func TestContext_BindAuto_XMLLeadingBOM(t *testing.T) {
	c := bindTestContext("\xEF\xBB\xBF"+`<item><name>a</name></item>`, "application/xml")
	var v struct {
		Name string `xml:"name"`
	}
	if err := c.BindAuto(&v); err != nil {
		t.Fatalf("BindAuto error = %v, want nil", err)
	}
	if v.Name != "a" {
		t.Errorf("Name = %q, want a", v.Name)
	}
}
