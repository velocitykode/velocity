package router

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
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

// TestContext_Bind_ClientErrors asserts a JSON body the client got wrong
// binds to a 400 carrying the decoder error, through Bind and the binders
// that reach it, while extra data stays ErrBindExtraData.
func TestContext_Bind_ClientErrors(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	isSyntax := func(err error) bool {
		var se *json.SyntaxError
		return errors.As(err, &se)
	}
	isType := func(err error) bool {
		var te *json.UnmarshalTypeError
		return errors.As(err, &te)
	}
	isUnexpectedEOF := func(err error) bool { return errors.Is(err, io.ErrUnexpectedEOF) }
	isEOF := func(err error) bool { return errors.Is(err, io.EOF) }
	binders := []struct {
		name string
		bind func(c *Context, v any) error
	}{
		{"Bind", func(c *Context, v any) error { return c.Bind(v) }},
		{"BindAuto", func(c *Context, v any) error { return c.BindAuto(v) }},
		{"BindValid", func(c *Context, v any) error { return c.BindValid(v) }},
	}
	tests := []struct {
		name        string
		body        string
		wantMessage string
		wantCause   func(error) bool
	}{
		{"syntax error", `{bad}`, "malformed request body", isSyntax},
		{"wrong type", `{"name":5}`, "malformed request body", isType},
		{"cut short", `{"name":`, "malformed request body", isUnexpectedEOF},
		{"empty body", ``, "empty request body", isEOF},
		{"whitespace only body", "  \n ", "empty request body", isEOF},
	}
	for _, b := range binders {
		for _, tt := range tests {
			t.Run(b.name+"/"+tt.name, func(t *testing.T) {
				c := bindTestContext(tt.body, "application/json")
				var p payload
				err := b.bind(c, &p)
				var he *contract.HTTPError
				if !errors.As(err, &he) {
					t.Fatalf("error = %T %v, want *contract.HTTPError", err, err)
				}
				if he.Status != http.StatusBadRequest || he.Message != tt.wantMessage {
					t.Errorf("HTTPError = %d %q, want 400 %q", he.Status, he.Message, tt.wantMessage)
				}
				if !tt.wantCause(he.Cause) {
					t.Errorf("cause = %T %v, want the decoder error", he.Cause, he.Cause)
				}
			})
		}
	}
}

// TestContext_Bind_OverLimitStaysMaxBytesError asserts a body over the
// limit is not turned into a 400: Bind returns the *http.MaxBytesError.
func TestContext_Bind_OverLimitStaysMaxBytesError(t *testing.T) {
	body := `{"name":"` + strings.Repeat("a", 64) + `"}`
	req := httptest.NewRequest("POST", "/test", strings.NewReader(body))
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.Request.Body = http.MaxBytesReader(w, c.Request.Body, 20)
	c.Set(bodyLimitKey, true)

	var p map[string]any
	err := c.Bind(&p)
	var tooLarge *http.MaxBytesError
	if !errors.As(err, &tooLarge) || error(tooLarge) != err {
		t.Fatalf("error = %T %v, want the *http.MaxBytesError itself", err, err)
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

// TestContext_BindXMLFormQuery_ClientErrors asserts an XML body, a form
// body or a query value the client got wrong binds to a 400 carrying the
// parse error, while a form body over the limit keeps its
// *http.MaxBytesError.
func TestContext_BindXMLFormQuery_ClientErrors(t *testing.T) {
	type item struct {
		XMLName xml.Name `xml:"item"`
		Name    string   `xml:"name"`
		Count   int      `xml:"count"`
	}
	type form struct {
		Name  string `form:"name" query:"name"`
		Count int    `form:"count" query:"count"`
	}
	isXMLSyntax := func(err error) bool {
		var se *xml.SyntaxError
		return errors.As(err, &se)
	}
	isNumber := func(err error) bool {
		var ne *strconv.NumError
		return errors.As(err, &ne)
	}
	isEscape := func(err error) bool {
		var ee url.EscapeError
		return errors.As(err, &ee)
	}
	isEOF := func(err error) bool { return errors.Is(err, io.EOF) }
	isAny := func(err error) bool { return err != nil }
	xmlBind := func(c *Context) error { var v item; return c.BindXML(&v) }
	formBind := func(c *Context) error { var v form; return c.BindForm(&v) }
	autoForm := func(c *Context) error { var v form; return c.BindAuto(&v) }
	tests := []struct {
		name        string
		contentType string
		body        string
		target      string
		bind        func(c *Context) error
		wantMessage string
		wantCause   func(error) bool
	}{
		{"xml syntax error", "application/xml", `<item><name>a</item>`, "/test", xmlBind, "malformed request body", isXMLSyntax},
		{"xml cut short", "application/xml", `<item><name>a`, "/test", xmlBind, "malformed request body", isXMLSyntax},
		{"xml value of the wrong type", "application/xml", `<item><count>many</count></item>`, "/test", xmlBind, "malformed request body", isNumber},
		{"xml empty body", "application/xml", ``, "/test", xmlBind, "empty request body", isEOF},
		{"xml whitespace only body", "application/xml", " \n ", "/test", xmlBind, "empty request body", isEOF},
		{"form bad escape", "application/x-www-form-urlencoded", "name=%zz", "/test", formBind, "malformed request body", isEscape},
		{"form malformed pair", "application/x-www-form-urlencoded", "name=a;count=1", "/test", formBind, "malformed request body", isAny},
		{"form value of the wrong type", "application/x-www-form-urlencoded", "count=many", "/test", formBind, "malformed request body", isNumber},
		{"form through BindAuto", "application/x-www-form-urlencoded", "name=%zz", "/test", autoForm, "malformed request body", isEscape},
		{"query value of the wrong type", "", "", "/test?count=many", func(c *Context) error { var v form; return c.BindQuery(&v) }, "malformed query string", isNumber},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", tt.target, strings.NewReader(tt.body))
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			c := NewContext(httptest.NewRecorder(), req)

			err := tt.bind(c)
			var he *contract.HTTPError
			if !errors.As(err, &he) {
				t.Fatalf("error = %T %v, want *contract.HTTPError", err, err)
			}
			if he.Status != http.StatusBadRequest || he.Message != tt.wantMessage {
				t.Errorf("HTTPError = %d %q, want 400 %q", he.Status, he.Message, tt.wantMessage)
			}
			if !tt.wantCause(he.Cause) {
				t.Errorf("cause = %T %v, want the parse error", he.Cause, he.Cause)
			}
		})
	}
}

// TestContext_BindForm_OverLimitStaysMaxBytesError asserts a form body
// over the limit is not turned into a 400: the error still wraps the
// *http.MaxBytesError and names no status of its own.
func TestContext_BindForm_OverLimitStaysMaxBytesError(t *testing.T) {
	body := "name=" + strings.Repeat("a", 64)
	req := httptest.NewRequest("POST", "/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.Request.Body = http.MaxBytesReader(w, c.Request.Body, 20)
	c.Set(bodyLimitKey, true)

	var v struct {
		Name string `form:"name"`
	}
	err := c.BindForm(&v)
	var tooLarge *http.MaxBytesError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("error = %T %v, want one wrapping *http.MaxBytesError", err, err)
	}
	if _, _, named := contract.StatusOf(err); named {
		t.Errorf("error %v names a status; the pipeline must map the *http.MaxBytesError to 413", err)
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
