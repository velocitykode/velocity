package inflect

import "testing"

func TestPlural(t *testing.T) {
	tests := []struct {
		in    string
		count []float64
		want  string
	}{
		{"user", nil, "users"},
		{"category", nil, "categories"},
		{"day", nil, "days"},
		{"box", nil, "boxes"},
		{"class", nil, "classes"},
		{"church", nil, "churches"},
		{"dish", nil, "dishes"},
		{"leaf", nil, "leaves"},
		{"knife", nil, "knives"},
		{"user", []float64{1}, "user"},
		{"user", []float64{2}, "users"},
		{"y", nil, "ys"},
		{"", nil, "s"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := Plural(tt.in, tt.count...); got != tt.want {
				t.Errorf("Plural(%q, %v) = %q; want %q", tt.in, tt.count, got, tt.want)
			}
		})
	}
}

func TestSingular(t *testing.T) {
	tests := []struct{ in, want string }{
		{"users", "user"},
		{"categories", "category"},
		{"boxes", "box"},
		{"classes", "class"},
		{"churches", "church"},
		{"dishes", "dish"},
		{"knives", "knife"},
		{"wolves", "wolf"},
		{"class", "class"},
		{"user", "user"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := Singular(tt.in); got != tt.want {
				t.Errorf("Singular(%q) = %q; want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDelimited(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		delimiter rune
		want      string
	}{
		{"empty", "", '_', ""},
		{"camel", "helloWorld", '_', "hello_world"},
		{"pascal", "HelloWorldExample", '_', "hello_world_example"},
		{"acronym", "HTTPServer", '_', "http_server"},
		{"acronym tail", "userID", '_', "user_id"},
		{"spaces and dots", "hello world.example", '-', "hello-world-example"},
		{"mixed delimiters", "hello_world-example", '-', "hello-world-example"},
		{"collapsed runs", "hello__world--example", '_', "hello_world_example"},
		{"trimmed", "_hello_", '_', "hello"},
		{"digits", "version2Beta", '_', "version2_beta"},
		{"unicode", "ÜberCool", '-', "über-cool"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Delimited(tt.in, tt.delimiter); got != tt.want {
				t.Errorf("Delimited(%q, %q) = %q; want %q", tt.in, tt.delimiter, got, tt.want)
			}
		})
	}
	if got := Snake("HelloWorld"); got != "hello_world" {
		t.Errorf("Snake = %q", got)
	}
	if got := Kebab("HelloWorld"); got != "hello-world" {
		t.Errorf("Kebab = %q", got)
	}
}
