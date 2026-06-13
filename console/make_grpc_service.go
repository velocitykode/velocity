package console

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	cli "github.com/velocitykode/velocity-cli"
	"github.com/velocitykode/velocity/console/stubs"
)

// MakeGRPCServiceOptions holds flags for the make:grpc:service command. The
// zero value reproduces the single-argument behaviour: the package leaf,
// proto wire package, file names, and impl directory are all derived from the
// service name, and the provider is wired automatically.
type MakeGRPCServiceOptions struct {
	// Package overrides the directory leaf under api/proto/ and api/gen/go/
	// (e.g. "admin"). When empty the leaf is derived from the service name.
	// This decouples the package from the service name so several services
	// can share one vendor-prefixed package.
	Package string
	// ProtoPackage sets the full wire package written to the .proto file
	// (e.g. "velship.admin.v1"). The directory leaf and import alias are
	// derived from its trailing segments. When empty it defaults to
	// "<leaf>.v1".
	ProtoPackage string
	// Dir overrides the Go impl output directory (default
	// internal/grpc/services).
	Dir string
	// Alias overrides the import alias used for the generated proto package
	// in the impl and provider files (default "<leaf>pb").
	Alias string
	// ProtoName overrides the proto file base name, without extension
	// (default: the lower-cased service base, e.g. "templatecontrol").
	ProtoName string
	// ImplName overrides the Go impl file base name, without extension
	// (default: the snake_case service base, e.g. "template_control").
	ImplName string
	// NoProvider skips provider scaffolding/wiring entirely (proto + impl
	// only). Useful when the app registers services through its own server
	// wiring rather than the generated internal/providers/grpc_provider.go.
	NoProvider bool
}

// grpcScaffold holds every resolved, validated value the writers need. It is
// produced once by resolveGRPCScaffold so the proto, impl, and provider stay
// in agreement on names, paths, and import aliases.
type grpcScaffold struct {
	ServiceName    string // Go service type, e.g. "TemplateControlService"
	Leaf           string // dir segment under api/proto + api/gen/go, e.g. "admin"
	Version        string // version segment, e.g. "v1"
	WirePackage    string // proto `package X;` line, e.g. "velship.admin.v1"
	GenPkgName     string // generated package name (go_package ";X"), e.g. "adminv1"
	Alias          string // import alias at call sites, e.g. "adminpb"
	ProtoFile      string // proto base name (no ext), e.g. "templatecontrol"
	ImplFile       string // impl base name (no ext), e.g. "template_control"
	ImplDir        string // impl output dir, e.g. "internal/shared/grpc/services"
	ServicesImport string // import path for the impl package
	ModulePath     string // module path from go.mod
	VarName        string // local var in the provider, e.g. "templateControl"
	NoProvider     bool
}

var (
	// protoSegRe matches a single lowercase proto/Go package identifier. It is
	// applied to every dot-segment of a wire package and to the resolved
	// directory leaf so the value is both a valid proto identifier and a
	// path-safe directory name (no "/", "..", or "." can slip through).
	protoSegRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	// goIdentRe matches a valid Go identifier, used to validate the import
	// alias before it is written into generated source.
	goIdentRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// MakeGRPCService scaffolds a new gRPC service: proto file, server impl, and
// (unless opts.NoProvider) provider wiring. It is safe to call repeatedly
// with different service names. The provider is created once and subsequent
// calls inject registrations at the // vel:grpc:imports and // vel:grpc:services
// marker comments.
func MakeGRPCService(name string, opts MakeGRPCServiceOptions) error {
	sc, err := resolveGRPCScaffold(name, opts)
	if err != nil {
		return err
	}

	// Preflight the provider wire before writing any files. The compatibility
	// guard (a service whose impl dir differs from the existing provider's)
	// must fail here, not after the proto and impl exist - otherwise the
	// suggested "re-run with --no-provider" would trip "proto already exists".
	if err := preflightProviderWiring(sc); err != nil {
		return err
	}

	// Create buf configs FIRST so a write failure here does not leave a
	// proto on disk that blocks the user from rerunning. The previous
	// order (proto then configs) could lock the command into "proto
	// already exists" on every subsequent attempt after a config-write
	// failure, with no impl or provider wired.
	if err := ensureBufConfigs(); err != nil {
		return err
	}
	if err := writeProtoFile(sc); err != nil {
		return err
	}
	if err := writeServiceImpl(sc); err != nil {
		return err
	}
	if !sc.NoProvider {
		if err := wireGRPCProvider(sc); err != nil {
			return err
		}
	}

	cli.Newline()
	cli.Muted("Next: vel make:grpc:gen  (generate Go code from .proto)")
	return nil
}

// resolveGRPCScaffold validates the service name and flags and computes every
// derived value. All filesystem-bound values (leaf, file names, impl dir) are
// validated against path traversal here so the writers can build paths
// without re-checking.
func resolveGRPCScaffold(name string, opts MakeGRPCServiceOptions) (grpcScaffold, error) {
	var sc grpcScaffold
	if err := validateMakeName(name); err != nil {
		return sc, err
	}
	sc.ServiceName = grpcServiceName(name)
	base := grpcBaseName(name)

	// Resolve the directory leaf and version. Priority: an explicit
	// --proto-package supplies both (its trailing segments), an explicit
	// --package overrides the leaf, and otherwise both fall back to the
	// service-name-derived defaults.
	version := "v1"
	var leaf string
	if opts.ProtoPackage != "" {
		if err := validateProtoPackage(opts.ProtoPackage); err != nil {
			return sc, fmt.Errorf("invalid --proto-package %q: %w", opts.ProtoPackage, err)
		}
		leaf, version = splitProtoPackage(opts.ProtoPackage)
		sc.WirePackage = opts.ProtoPackage
	}
	if opts.Package != "" {
		if strings.Contains(opts.Package, "/") {
			return sc, fmt.Errorf("invalid --package %q: must be a single path segment", opts.Package)
		}
		leaf = strings.ToLower(opts.Package)
	}
	if leaf == "" {
		leaf = grpcPackageName(name)
	}
	if err := validateLeaf(leaf); err != nil {
		return sc, fmt.Errorf("package leaf %q: %w", leaf, err)
	}
	sc.Leaf = leaf
	sc.Version = version
	if sc.WirePackage == "" {
		sc.WirePackage = leaf + "." + version
	}
	// The generated package name (the ";X" suffix of go_package) is fixed to
	// "<leaf><version>"; every importer and the gateway reference it by that
	// name. Only the call-site alias is configurable.
	sc.GenPkgName = leaf + version

	alias := grpcProtoAlias(leaf)
	if opts.Alias != "" {
		alias = opts.Alias
	}
	if err := validateGoIdent(alias); err != nil {
		return sc, fmt.Errorf("invalid --alias %q: %w", alias, err)
	}
	sc.Alias = alias

	protoFile := strings.ToLower(base)
	if opts.ProtoName != "" {
		protoFile = opts.ProtoName
	}
	if err := validateFileBase(protoFile); err != nil {
		return sc, fmt.Errorf("proto file name %q: %w", protoFile, err)
	}
	sc.ProtoFile = protoFile

	implFile := toSnakeCase(base)
	if opts.ImplName != "" {
		implFile = opts.ImplName
	}
	if err := validateFileBase(implFile); err != nil {
		return sc, fmt.Errorf("impl file name %q: %w", implFile, err)
	}
	sc.ImplFile = implFile

	// --dir is a relative output directory override; resolveMakeDir validates
	// (nested segments allowed), cleans, and confirms it stays in the tree.
	implDir, err := resolveMakeDir(filepath.Join("internal", "grpc", "services"), opts.Dir)
	if err != nil {
		return sc, err
	}
	sc.ImplDir = implDir

	modulePath, err := readModulePath()
	if err != nil {
		return sc, err
	}
	sc.ModulePath = modulePath
	sc.ServicesImport = modulePath + "/" + filepath.ToSlash(implDir)

	sc.VarName = lowerFirst(base)
	sc.NoProvider = opts.NoProvider
	return sc, nil
}

// splitProtoPackage extracts the directory leaf and version from a full wire
// package. A trailing "vN" segment is treated as the version and the segment
// before it as the leaf ("velship.admin.v1" -> "admin", "v1"); without a
// version suffix the last segment is the leaf and the version defaults to v1
// ("foo" -> "foo", "v1").
func splitProtoPackage(pkg string) (leaf, version string) {
	segs := strings.Split(pkg, ".")
	version = "v1"
	n := len(segs)
	if n == 0 {
		return "", version
	}
	if last := segs[n-1]; isVersionSeg(last) && n >= 2 {
		return segs[n-2], last
	}
	return segs[n-1], version
}

// isVersionSeg reports whether s is a proto version segment: "v" followed by
// one or more digits (v1, v2, v10).
func isVersionSeg(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// validateProtoPackage ensures every dot-segment of a wire package is a valid
// lowercase proto identifier. This both rejects malformed packages early and
// prevents arbitrary text (newlines, semicolons) from being written into the
// generated .proto.
func validateProtoPackage(pkg string) error {
	if pkg == "" {
		return fmt.Errorf("must not be empty")
	}
	for _, seg := range strings.Split(pkg, ".") {
		if !protoSegRe.MatchString(seg) {
			return fmt.Errorf("segment %q is not a valid lowercase identifier", seg)
		}
	}
	return nil
}

// validateLeaf ensures the resolved directory leaf is a single lowercase
// identifier. Because the leaf feeds api/proto/<leaf>/ and api/gen/go/<leaf>/
// as well as the generated package name, this guarantees both path safety and
// a valid Go/proto package identifier.
func validateLeaf(leaf string) error {
	if !protoSegRe.MatchString(leaf) {
		return fmt.Errorf("must be a lowercase identifier matching [a-z][a-z0-9_]*")
	}
	return nil
}

// validateGoIdent ensures the import alias is a valid Go identifier before it
// is written into generated source.
func validateGoIdent(s string) error {
	if !goIdentRe.MatchString(s) {
		return fmt.Errorf("must be a valid Go identifier")
	}
	return nil
}

// validateFileBase ensures a file base name is a single, traversal-safe path
// segment. validateMakeName rejects "..", absolute paths, hidden segments,
// backslashes, NUL, and "/", so the base cannot smuggle in a subdirectory.
func validateFileBase(s string) error {
	return validateMakeName(s)
}

// lowerFirst lower-cases the first rune of s, producing a lowerCamelCase local
// variable name from the PascalCase service base.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func writeProtoFile(sc grpcScaffold) error {
	protoRoot := filepath.Join("api", "proto")
	dir := filepath.Join(protoRoot, sc.Leaf, sc.Version)
	if err := ensureWithinRoot(protoRoot, dir); err != nil {
		return fmt.Errorf("invalid package leaf %q: %w", sc.Leaf, err)
	}
	if err := os.MkdirAll(dir, defaultDirMode); err != nil {
		return fmt.Errorf("create proto dir: %w", err)
	}
	path := filepath.Join(dir, sc.ProtoFile+".proto")
	if err := ensureWritableTarget(path, "proto"); err != nil {
		return err
	}

	stub, err := stubs.Get("grpc/proto.proto.stub")
	if err != nil {
		return fmt.Errorf("load proto stub: %w", err)
	}
	tmpl, err := template.New("proto").Parse(string(stub))
	if err != nil {
		return fmt.Errorf("parse proto stub: %w", err)
	}

	data := map[string]interface{}{
		"WirePackage": sc.WirePackage,
		"ServiceName": sc.ServiceName,
		"ModulePath":  sc.ModulePath,
		"Leaf":        sc.Leaf,
		"Version":     sc.Version,
		"GenPkgName":  sc.GenPkgName,
		"RPCs":        []map[string]string{},
		"Messages":    []map[string]string{},
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("render proto: %w", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), defaultFileMode); err != nil {
		return fmt.Errorf("write proto: %w", err)
	}
	cli.Success(fmt.Sprintf("Created: %s", path))
	return nil
}

// ensureBufConfigs writes api/proto/buf.yaml and api/proto/buf.gen.yaml on
// first run so `vel make:grpc:gen` works out of the box. Existing files are
// left untouched. Write failures are propagated, since the scaffolder
// otherwise reports success while leaving generation broken.
func ensureBufConfigs() error {
	protoRoot := filepath.Join("api", "proto")
	if err := os.MkdirAll(protoRoot, defaultDirMode); err != nil {
		return fmt.Errorf("create %s: %w", protoRoot, err)
	}

	bufYaml := filepath.Join(protoRoot, "buf.yaml")
	if _, err := os.Stat(bufYaml); os.IsNotExist(err) {
		content := `version: v2
modules:
  - path: .
lint:
  use:
    - STANDARD
breaking:
  use:
    - FILE
`
		if err := os.WriteFile(bufYaml, []byte(content), defaultFileMode); err != nil {
			return fmt.Errorf("write %s: %w", bufYaml, err)
		}
		cli.Success(fmt.Sprintf("Created: %s", bufYaml))
	}

	bufGen := filepath.Join(protoRoot, "buf.gen.yaml")
	if _, err := os.Stat(bufGen); os.IsNotExist(err) {
		content := `version: v2
plugins:
  - remote: buf.build/protocolbuffers/go
    out: ../gen/go
    opt: paths=source_relative
  - remote: buf.build/grpc/go
    out: ../gen/go
    opt:
      - paths=source_relative
      - require_unimplemented_servers=false
`
		if err := os.WriteFile(bufGen, []byte(content), defaultFileMode); err != nil {
			return fmt.Errorf("write %s: %w", bufGen, err)
		}
		cli.Success(fmt.Sprintf("Created: %s", bufGen))
	}
	return nil
}

func writeServiceImpl(sc grpcScaffold) error {
	dir := sc.ImplDir
	if err := os.MkdirAll(dir, defaultDirMode); err != nil {
		return fmt.Errorf("create services dir: %w", err)
	}
	path := filepath.Join(dir, sc.ImplFile+".go")
	if err := ensureWithinRoot(dir, path); err != nil {
		return fmt.Errorf("invalid impl file name %q: %w", sc.ImplFile, err)
	}
	// Guard the custom --dir against escaping the project root. validateMakeName
	// already rejected "../" and absolute dirs, but resolving against the
	// working directory is the authoritative path-traversal check.
	if err := ensureWithinRoot(".", path); err != nil {
		return fmt.Errorf("invalid --dir %q: %w", dir, err)
	}
	if err := ensureWritableTarget(path, "service"); err != nil {
		return err
	}

	stub, err := stubs.Get("grpc/service.go.stub")
	if err != nil {
		return fmt.Errorf("load service stub: %w", err)
	}
	tmpl, err := template.New("service").Parse(string(stub))
	if err != nil {
		return fmt.Errorf("parse service stub: %w", err)
	}

	data := map[string]interface{}{
		"Alias":       sc.Alias,
		"ServiceName": sc.ServiceName,
		"ModulePath":  sc.ModulePath,
		"Leaf":        sc.Leaf,
		"Version":     sc.Version,
		"Methods":     []map[string]string{},
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("render service: %w", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), defaultFileMode); err != nil {
		return fmt.Errorf("write service: %w", err)
	}
	cli.Success(fmt.Sprintf("Created: %s", path))
	return nil
}
