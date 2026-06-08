package velocity

import (
	"fmt"
	"strings"

	cli "github.com/velocitykode/velocity-cli"
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
	cli.Newline()
	cli.Muted("Usage: vel make:grpc:service [Name] [flags]")
	cli.Newline()
	cli.Muted("Flags:")
	cli.Muted("  --package <leaf>          dir leaf under api/proto and api/gen/go (default: from Name)")
	cli.Muted("  --proto-package <pkg>     full wire package, e.g. velship.admin.v1 (default: <leaf>.v1)")
	cli.Muted("  --dir <path>              Go impl output dir (default: internal/grpc/services)")
	cli.Muted("  --alias <ident>           proto import alias (default: <leaf>pb)")
	cli.Muted("  --proto-name <base>       proto file base name (default: lowercased Name)")
	cli.Muted("  --impl-name <base>        impl file base name (default: snake_case Name)")
	cli.Muted("  --no-provider             skip provider scaffolding/wiring")
	cli.Newline()
	cli.Muted("Examples:")
	cli.Muted("  vel make:grpc:service Foo")
	cli.Muted("  vel make:grpc:service TemplateControl --package admin \\")
	cli.Muted("    --proto-package velship.admin.v1 --dir internal/shared/grpc/services --no-provider")
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
				if i+1 >= len(args) {
					return "", fmt.Errorf("flag %s needs a value", key)
				}
				i++
				return args[i], nil
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
func (makeGRPCRPCCmd) run(a *App, args []string) error {
	if len(args) < 2 {
		cli.Newline()
		cli.Muted("Usage: vel make:grpc:rpc [Service] [RPC] [--stream|--client-stream|--bidi]")
		cli.Newline()
		cli.Muted("Examples:")
		cli.Muted("  vel make:grpc:rpc Foo Hello")
		cli.Muted("  vel make:grpc:rpc Foo Tail --stream")
		cli.Muted("  vel make:grpc:rpc Foo Upload --client-stream")
		cli.Muted("  vel make:grpc:rpc Foo Chat --bidi")
		return fmt.Errorf("service and rpc name are required")
	}
	opts := console.MakeGRPCRPCOptions{}
	for _, arg := range args[2:] {
		switch arg {
		case "--stream", "--server-stream":
			opts.Stream = true
		case "--client-stream":
			opts.ClientStream = true
		case "--bidi", "--bidirectional":
			opts.Bidi = true
		}
	}
	return console.MakeGRPCRPC(args[0], args[1], opts)
}

type makeGRPCGenCmd struct{}

func (makeGRPCGenCmd) name() string {
	return "make:grpc:gen"
}
func (makeGRPCGenCmd) description() string {
	return "Run `buf generate` in api/proto"
}
func (makeGRPCGenCmd) run(a *App, args []string) error {
	return console.MakeGRPCGen()
}
