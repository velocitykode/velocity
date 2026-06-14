package velocity

import (
	"fmt"
	"strings"

	"github.com/velocitykode/prism"
	"github.com/velocitykode/velocity/console"
)

type makeGRPCServiceCmd struct{}

func (makeGRPCServiceCmd) name() string {
	return "make:grpc:service"
}
func (makeGRPCServiceCmd) description() string {
	return "Scaffold a new gRPC service (proto + impl + provider wire)"
}
func (makeGRPCServiceCmd) run(a *App, args []string) error {
	name, opts, err := parseMakeGRPCServiceArgs(args)
	if err != nil {
		grpcServiceUsage()
		return err
	}
	if name == "" {
		grpcServiceUsage()
		return fmt.Errorf("service name is required")
	}
	return console.MakeGRPCService(name, opts)
}

func grpcServiceUsage() {
	prism.Newline()
	prism.Muted("Usage: vel make:grpc:service [Name] [flags]")
	prism.Newline()
	prism.Muted("Flags:")
	prism.Muted("  --package <leaf>          dir leaf under api/proto and api/gen/go (default: from Name)")
	prism.Muted("  --proto-package <pkg>     full wire package, e.g. velship.admin.v1 (default: <leaf>.v1)")
	prism.Muted("  --dir <path>              Go impl output dir (default: internal/grpc/services)")
	prism.Muted("  --alias <ident>           proto import alias (default: <leaf>pb)")
	prism.Muted("  --proto-name <base>       proto file base name (default: lowercased Name)")
	prism.Muted("  --impl-name <base>        impl file base name (default: snake_case Name)")
	prism.Muted("  --no-provider             skip provider scaffolding/wiring")
	prism.Newline()
	prism.Muted("Examples:")
	prism.Muted("  vel make:grpc:service Foo")
	prism.Muted("  vel make:grpc:service TemplateControl --package admin \\")
	prism.Muted("    --proto-package velship.admin.v1 --dir internal/shared/grpc/services --no-provider")
}

// parseMakeGRPCServiceArgs parses the positional service name and the optional
// flags. Flags may appear before or after the name and accept either
// "--flag value" or "--flag=value" forms. Unknown flags are rejected so a
// typo does not silently fall through as the service name.
func parseMakeGRPCServiceArgs(args []string) (string, console.MakeGRPCServiceOptions, error) {
	var (
		name string
		opts console.MakeGRPCServiceOptions
	)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--no-provider" {
			opts.NoProvider = true
			continue
		}
		if strings.HasPrefix(arg, "--") {
			key, val, hasEq := strings.Cut(arg, "=")
			value := func() (string, error) {
				if hasEq {
					return val, nil
				}
				v, ni, err := spacedValue(args, i, key)
				if err != nil {
					return "", err
				}
				i = ni
				return v, nil
			}
			var err error
			switch key {
			case "--package":
				opts.Package, err = value()
			case "--proto-package":
				opts.ProtoPackage, err = value()
			case "--dir":
				opts.Dir, err = value()
			case "--alias":
				opts.Alias, err = value()
			case "--proto-name":
				opts.ProtoName, err = value()
			case "--impl-name":
				opts.ImplName, err = value()
			default:
				return "", opts, fmt.Errorf("unknown flag: %s", key)
			}
			if err != nil {
				return "", opts, err
			}
			continue
		}
		// A single-dash token here is a flag typo, not the service name
		// (double-dash tokens are handled above): reject it.
		if strings.HasPrefix(arg, "-") {
			return "", opts, fmt.Errorf("unknown flag: %s", arg)
		}
		if name != "" {
			return "", opts, fmt.Errorf("unexpected argument: %s", arg)
		}
		name = arg
	}
	return name, opts, nil
}

type makeGRPCRPCCmd struct{}

func (makeGRPCRPCCmd) name() string {
	return "make:grpc:rpc"
}
func (makeGRPCRPCCmd) description() string {
	return "Add an rpc to an existing gRPC service"
}
func grpcRPCUsage() {
	prism.Newline()
	prism.Muted("Usage: vel make:grpc:rpc [Service] [RPC] [--stream|--client-stream|--bidi]")
	prism.Newline()
	prism.Muted("Examples:")
	prism.Muted("  vel make:grpc:rpc Foo Hello")
	prism.Muted("  vel make:grpc:rpc Foo Tail --stream")
	prism.Muted("  vel make:grpc:rpc Foo Upload --client-stream")
	prism.Muted("  vel make:grpc:rpc Foo Chat --bidi")
}

func (makeGRPCRPCCmd) run(a *App, args []string) error {
	if len(args) < 2 {
		grpcRPCUsage()
		return fmt.Errorf("service and rpc name are required")
	}
	// The first two positionals are the service and rpc names; a flag-like
	// token in either slot is a typo, not a name.
	for _, n := range args[:2] {
		if strings.HasPrefix(n, "-") {
			grpcRPCUsage()
			return unknownToken(n, n)
		}
	}
	opts, err := parseMakeGRPCRPCArgs(args[2:])
	if err != nil {
		grpcRPCUsage()
		return err
	}
	return console.MakeGRPCRPC(args[0], args[1], opts)
}

// parseMakeGRPCRPCArgs parses the streaming flags that follow the service and
// rpc names. Unknown flags and stray positionals (a third name) are rejected.
func parseMakeGRPCRPCArgs(args []string) (console.MakeGRPCRPCOptions, error) {
	var opts console.MakeGRPCRPCOptions
	for _, arg := range args {
		switch arg {
		case "--stream", "--server-stream":
			opts.Stream = true
		case "--client-stream":
			opts.ClientStream = true
		case "--bidi", "--bidirectional":
			opts.Bidi = true
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, fmt.Errorf("unknown flag: %s", arg)
			}
			return opts, fmt.Errorf("unexpected argument: %s", arg)
		}
	}
	return opts, nil
}

type makeGRPCGenCmd struct{}

func (makeGRPCGenCmd) name() string {
	return "make:grpc:gen"
}
func (makeGRPCGenCmd) description() string {
	return "Run `buf generate` in api/proto"
}
func (makeGRPCGenCmd) run(a *App, args []string) error {
	if err := rejectNoArgs(args); err != nil {
		return err
	}
	return console.MakeGRPCGen()
}
