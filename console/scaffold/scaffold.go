package scaffold

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"unicode"
)

const (
	defaultFileMode os.FileMode = 0644
	defaultDirMode  os.FileMode = 0755
)

// Generator scaffolds one source file from a text/template stub.
type Generator struct {
	DefaultDir string
	Kind       string
	Stub       string
	Filename   string
}

// Result describes the files written by a generator.
type Result struct {
	// Path is the primary file written by the generator.
	Path string
	// Paths contains every file written. For single-file generators it contains
	// exactly Path; it is reserved for additive multi-file generator support.
	Paths []string
}

// Generate validates name, resolves the output directory, renders Stub with
// data, and writes the generated file without overwriting an existing target.
func (g Generator) Generate(name, dirOverride string, data map[string]any) (Result, error) {
	if err := ValidateName(name); err != nil {
		return Result{}, err
	}

	outputDir, err := ResolveDir(g.DefaultDir, dirOverride)
	if err != nil {
		return Result{}, err
	}

	filename := g.Filename
	if filename == "" {
		filename = SnakeCase(PascalCase(name)) + ".go"
	}

	path, err := write(outputDir, filename, g.Kind, g.Stub, data)
	if err != nil {
		return Result{}, err
	}

	return Result{Path: path, Paths: []string{path}}, nil
}

func write(outputDir, filename, kind, stub string, data map[string]any) (string, error) {
	if err := os.MkdirAll(outputDir, defaultDirMode); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	outputPath := filepath.Join(outputDir, filename)
	if err := EnsureWithinRoot(outputDir, outputPath); err != nil {
		return "", fmt.Errorf("invalid %s filename %q: %w", kind, filename, err)
	}
	if err := EnsureWritableTarget(outputPath, kind); err != nil {
		return "", err
	}

	content, err := render(kind, stub, data)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(outputPath, content, defaultFileMode); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}
	return outputPath, nil
}

func render(name, stub string, data map[string]any) ([]byte, error) {
	tmpl, err := template.New(name).Parse(stub)
	if err != nil {
		return nil, fmt.Errorf("failed to parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("failed to execute template: %w", err)
	}
	return buf.Bytes(), nil
}

// ValidateName rejects scaffolder name arguments that would let a caller
// escape the intended output directory or smuggle arbitrary text into
// generated source. A name must be a single safe ASCII identifier segment.
func ValidateName(name string) error {
	if err := ValidateNestedName(name); err != nil {
		return err
	}
	if strings.Contains(name, "/") {
		return fmt.Errorf("invalid name %q: must be a single path segment (no '/')", name)
	}
	return nil
}

// ValidateNestedName applies the same rules as ValidateName except that
// slashes are allowed and each slash-separated segment is validated
// independently.
func ValidateNestedName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if strings.ContainsRune(name, 0) {
		return fmt.Errorf("invalid name %q: contains NUL byte", name)
	}
	if strings.Contains(name, "\\") {
		return fmt.Errorf("invalid name %q: backslash not allowed", name)
	}
	if filepath.IsAbs(name) {
		return fmt.Errorf("invalid name %q: absolute paths not allowed", name)
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" {
			return fmt.Errorf("invalid name %q: empty path segment not allowed", name)
		}
		if seg == ".." {
			return fmt.Errorf("invalid name %q: parent traversal not allowed", name)
		}
		if strings.HasPrefix(seg, ".") {
			return fmt.Errorf("invalid name %q: segment %q starts with '.'", name, seg)
		}
		if err := validateNameSegment(name, seg); err != nil {
			return err
		}
	}
	return nil
}

func validateNameSegment(name, seg string) error {
	for i, r := range seg {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
		case i > 0 && ((r >= '0' && r <= '9') || r == '_' || r == '-'):
		default:
			if i == 0 {
				return fmt.Errorf("invalid name %q: segment %q must start with a letter (A-Z, a-z), found %q", name, seg, r)
			}
			return fmt.Errorf("invalid name %q: character %q not allowed (segments must match [A-Za-z][A-Za-z0-9_-]*)", name, r)
		}
	}
	return nil
}

// ResolveDir resolves a project-relative output directory override.
func ResolveDir(defaultDir, override string) (string, error) {
	if override == "" {
		return defaultDir, nil
	}
	if err := ValidateNestedName(override); err != nil {
		return "", fmt.Errorf("invalid --dir %q: %w", override, err)
	}
	dir := filepath.Clean(override)
	if err := EnsureWithinRoot(".", dir); err != nil {
		return "", fmt.Errorf("invalid --dir %q: %w", override, err)
	}
	if err := EnsureNoSymlinkComponents(dir); err != nil {
		return "", fmt.Errorf("invalid --dir %q: %w", override, err)
	}
	return dir, nil
}

// EnsureWritableTarget rejects existing files and final-path symlinks.
func EnsureWritableTarget(path, kind string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect %s path %q: %w", kind, path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s path %q is a symlink", kind, path)
	}
	return fmt.Errorf("%s already exists: %s", kind, path)
}

// EnsureNoSymlinkComponents rejects any existing symlink in a directory path.
func EnsureNoSymlinkComponents(dir string) error {
	cur := "."
	for _, seg := range strings.Split(dir, string(os.PathSeparator)) {
		cur = filepath.Join(cur, seg)
		info, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("inspect path %q: %w", cur, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path component %q is a symlink", cur)
		}
	}
	return nil
}

// EnsureWithinRoot verifies path resolves inside root after lexical cleaning.
func EnsureWithinRoot(root, path string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve root %q: %w", root, err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve path %q: %w", path, err)
	}
	prefix := absRoot + string(os.PathSeparator)
	if absPath != absRoot && !strings.HasPrefix(absPath, prefix) {
		return fmt.Errorf("path %q escapes root %q", absPath, absRoot)
	}
	return nil
}

// PascalCase converts a validated scaffold name to PascalCase.
func PascalCase(s string) string {
	words := splitWords(s)
	for i, word := range words {
		word = strings.ToLower(word)
		if word == "" {
			continue
		}
		words[i] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, "")
}

// SnakeCase converts a validated scaffold name to snake_case.
func SnakeCase(s string) string {
	var result []byte
	runes := []rune(s)
	for i, r := range runes {
		if i > 0 && r >= 'A' && r <= 'Z' {
			prev := runes[i-1]
			prevLower := prev >= 'a' && prev <= 'z'
			prevUpper := prev >= 'A' && prev <= 'Z'
			prevDigit := prev >= '0' && prev <= '9'
			nextLower := i+1 < len(runes) && runes[i+1] >= 'a' && runes[i+1] <= 'z'
			if prevLower || (prevUpper && nextLower) || prevDigit {
				result = append(result, '_')
			}
		}
		result = append(result, []byte(strings.ToLower(string(r)))...)
	}
	return string(result)
}

func splitWords(s string) []string {
	var words []string
	var current []rune

	for _, r := range s {
		if r == '_' || r == '-' || r == ' ' {
			if len(current) > 0 {
				words = append(words, string(current))
				current = nil
			}
		} else if unicode.IsUpper(r) && len(current) > 0 {
			words = append(words, string(current))
			current = []rune{r}
		} else {
			current = append(current, r)
		}
	}

	if len(current) > 0 {
		words = append(words, string(current))
	}

	return words
}
