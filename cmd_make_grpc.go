package velocity

import (
	"fmt"

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
	if len(args) == 0 {
		cli.Newline()
		cli.Muted("Usage: vel make:grpc:service [Name]")
		cli.Newline()
		cli.Muted("Examples:")
		cli.Muted("  vel make:grpc:service Foo")
		return fmt.Errorf("service name is required")
	}
	return console.MakeGRPCService(args[0], console.MakeGRPCServiceOptions{})
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
