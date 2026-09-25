package contract

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWantsJSON(t *testing.T) {
	tests := []struct {
		name    string
		accept  string
		xhr     string
		inertia string
		want    bool
	}{
		{"no headers", "", "", "", false},
		{"json", "application/json", "", "", true},
		{"json first", "application/json, text/html", "", "", true},
		{"html first then json", "text/html, application/json", "", "", false},
		{"browser default", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", "", "", false},
		{"json with parameters", "application/json; charset=utf-8", "", "", true},
		{"json with lower q", "application/json;q=0.9, text/html", "", "", false},
		{"case and spaces", "  Application/JSON  ", "", "", true},
		{"problem json suffix", "application/problem+json", "", "", true},
		{"vendor json suffix", "application/vnd.api+json;q=1, text/html", "", "", true},
		{"json suffix second", "text/html, application/problem+json", "", "", false},
		{"json-like subtype", "application/jsonx", "", "", false},
		{"xml", "application/xml", "", "", false},
		{"wildcard alone", "*/*", "", "", false},
		{"xhr empty accept", "", "XMLHttpRequest", "", true},
		{"xhr wildcard accept", "*/*", "XMLHttpRequest", "", true},
		{"xhr wildcard first", "*/*, text/html", "XMLHttpRequest", "", true},
		{"xhr wildcard with q", "*/*;q=0.8", "XMLHttpRequest", "", true},
		{"xhr lower case", "", "xmlhttprequest", "", true},
		{"xhr html accept", "text/html", "XMLHttpRequest", "", false},
		{"xhr other value", "", "fetch", "", false},
		{"inertia json accept", "application/json", "", "true", false},
		{"inertia xhr", "", "XMLHttpRequest", "true", false},
		{"inertia problem json", "application/problem+json", "XMLHttpRequest", "true", false},
		{"inertia upper case", "application/json", "", "TRUE", false},
		{"x-inertia 1 is not inertia", "application/json", "", "1", true},
		{"x-inertia false is not inertia", "application/json", "", "false", true},
		{"html preferred by q", "application/json;q=0.5, text/html", "", "", false},
		{"json preferred by q", "text/html;q=0.9, application/json", "", "", true},
		{"tie keeps listed order", "text/html;q=0.8, application/json;q=0.8", "", "", false},
		{"q zero excluded", "application/json;q=0, text/html;q=0.1", "", "", false},
		{"q zero html excluded", "text/html;q=0, application/json;q=0.1", "", "", true},
		{"upper case q", "text/html;Q=0.2, application/json", "", "", true},
		{"unparsable q is one", "text/html;q=abc, application/json;q=0.9", "", "", false},
		{"everything excluded xhr", "application/json;q=0", "XMLHttpRequest", "", true},
		{"wildcard preferred xhr", "text/html;q=0.5, */*", "XMLHttpRequest", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.accept != "" {
				r.Header.Set("Accept", tt.accept)
			}
			if tt.xhr != "" {
				r.Header.Set("X-Requested-With", tt.xhr)
			}
			if tt.inertia != "" {
				r.Header.Set("X-Inertia", tt.inertia)
			}
			if got := WantsJSON(r); got != tt.want {
				t.Errorf("WantsJSON() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWantsJSON_NilRequest(t *testing.T) {
	if WantsJSON(nil) {
		t.Error("WantsJSON(nil) = true, want false")
	}
}

func TestIsInertia(t *testing.T) {
	tests := []struct {
		name  string
		value string
		set   bool
		want  bool
	}{
		{"true", "true", true, true},
		{"upper case", "TRUE", true, true},
		{"mixed case", "True", true, true},
		{"one", "1", true, false},
		{"false", "false", true, false},
		{"other value", "yes", true, false},
		{"empty value", "", true, false},
		{"absent", "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.set {
				r.Header["X-Inertia"] = []string{tt.value}
			}
			if got := IsInertia(r); got != tt.want {
				t.Errorf("IsInertia() = %v, want %v", got, tt.want)
			}
		})
	}
	if IsInertia(nil) {
		t.Error("IsInertia(nil) = true, want false")
	}
}

func TestParseAccept(t *testing.T) {
	tests := []struct {
		accept string
		want   []MediaRange
	}{
		{"", []MediaRange{}},
		{" , ,", []MediaRange{}},
		{"text/html", []MediaRange{{"text/html", 1}}},
		{"Text/HTML;charset=utf-8", []MediaRange{{"text/html", 1}}},
		{"text/html;q=0.9, application/json", []MediaRange{{"application/json", 1}, {"text/html", 0.9}}},
		{"a/a;q=0.5, b/b;q=0.5, c/c", []MediaRange{{"c/c", 1}, {"a/a", 0.5}, {"b/b", 0.5}}},
		{"a/a;q=0, b/b", []MediaRange{{"b/b", 1}}},
		{"a/a;level=1;q=0.3", []MediaRange{{"a/a", 0.3}}},
		{"a/a;q=2", []MediaRange{{"a/a", 1}}},
		{"a/a;q=-1, b/b;q=NaN", []MediaRange{{"b/b", 1}}},
	}
	for _, tt := range tests {
		t.Run(tt.accept, func(t *testing.T) {
			got := ParseAccept(tt.accept)
			if len(got) != len(tt.want) {
				t.Fatalf("ParseAccept(%q) = %v, want %v", tt.accept, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("ParseAccept(%q)[%d] = %v, want %v", tt.accept, i, got[i], tt.want[i])
				}
			}
			first := ""
			if len(got) > 0 {
				first = got[0].Type
			}
			if p := PreferredMediaRange(tt.accept); p != first {
				t.Errorf("PreferredMediaRange(%q) = %q, want ParseAccept's first %q", tt.accept, p, first)
			}
		})
	}
}

func TestPreferredMediaRange_DoesNotAllocate(t *testing.T) {
	accept := "text/html;q=0.9, application/json, */*;q=0.1"
	allocs := testing.AllocsPerRun(100, func() {
		if PreferredMediaRange(accept) != "application/json" {
			t.Fatal("wrong preferred range")
		}
	})
	if allocs != 0 {
		t.Errorf("PreferredMediaRange allocated %v times, want 0", allocs)
	}
}

func TestAppendVary(t *testing.T) {
	tests := []struct {
		name  string
		start []string
		add   []string
		want  []string
	}{
		{name: "empty", add: []string{"Accept"}, want: []string{"Accept"}},
		{name: "no duplicate", add: []string{"X-Inertia", "X-Inertia", "x-inertia"}, want: []string{"X-Inertia"}},
		{name: "listed in a combined value", start: []string{"Origin, accept"}, add: []string{"Accept"}, want: []string{"Origin, accept"}},
		{name: "appends another", start: []string{"Origin"}, add: []string{"Accept"}, want: []string{"Origin", "Accept"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			if tt.start != nil {
				h["Vary"] = append([]string(nil), tt.start...)
			}
			for _, v := range tt.add {
				AppendVary(h, v)
			}
			got := h.Values("Vary")
			if len(got) != len(tt.want) {
				t.Fatalf("Vary = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("Vary = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// TestNegotiationHeaders_AreTheHeadersNegotiationReads asserts each header
// JSONNegotiationHeaders lists can flip WantsJSON on its own, and
// InertiaNegotiationHeader flips IsInertia, so a writer declaring them in
// Vary covers every header the negotiation reads.
func TestNegotiationHeaders_AreTheHeadersNegotiationReads(t *testing.T) {
	values := map[string]string{
		"Accept":           "application/json",
		"X-Requested-With": "XMLHttpRequest",
	}
	for _, name := range JSONNegotiationHeaders() {
		t.Run(name, func(t *testing.T) {
			value, ok := values[name]
			if !ok {
				t.Fatalf("no probe value for %s", name)
			}
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			before := WantsJSON(r)
			r.Header.Set(name, value)
			if WantsJSON(r) == before {
				t.Errorf("setting %s: %s left WantsJSON at %v", name, value, before)
			}
		})
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set(InertiaNegotiationHeader, "true")
	if !IsInertia(r) {
		t.Errorf("%s: true is not an Inertia request", InertiaNegotiationHeader)
	}
}
