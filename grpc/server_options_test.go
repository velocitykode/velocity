package grpc

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"google.golang.org/grpc/keepalive"

	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/log"
)

// No exported option of the package takes a grpc-go type other than the
// typed transport settings it names: a raw grpc.ServerOption, interceptor
// or stats handler could install code outside the call lifecycle, which
// must stay the outermost interceptor of every call.
func TestServerOptions_TakeNoRawGRPCType(t *testing.T) {
	allowed := map[string]bool{
		"credentials.TransportCredentials": true,
		"keepalive.ServerParameters":       true,
		"keepalive.EnforcementPolicy":      true,
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		// The local names of the grpc-go packages this file imports.
		grpcNames := map[string]bool{}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if !strings.HasPrefix(path, "google.golang.org/grpc") {
				continue
			}
			local := path[strings.LastIndex(path, "/")+1:]
			if imp.Name != nil {
				local = imp.Name.Name
			}
			grpcNames[local] = true
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !fn.Name.IsExported() || fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
				continue
			}
			if id, ok := fn.Type.Results.List[0].Type.(*ast.Ident); !ok || id.Name != "ServerOption" {
				continue
			}
			checked++
			for _, p := range fn.Type.Params.List {
				ast.Inspect(p.Type, func(n ast.Node) bool {
					sel, ok := n.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					pkg, ok := sel.X.(*ast.Ident)
					if !ok || !grpcNames[pkg.Name] {
						return true
					}
					if typ := pkg.Name + "." + sel.Sel.Name; !allowed[typ] {
						t.Errorf("%s takes %s: a raw grpc-go value could bypass the call lifecycle", fn.Name.Name, typ)
					}
					return false
				})
			}
		}
	}
	if checked == 0 {
		t.Fatal("found no ServerOption constructors to check")
	}
}

// rawClient is an HTTP/2 connection to a server that speaks only frames,
// so a test can send pings grpc-go's client would pace.
type rawClient struct {
	conn net.Conn
	fr   *http2.Framer
}

func dialRaw(t *testing.T, addr string) *rawClient {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Write([]byte(http2.ClientPreface)); err != nil {
		t.Fatalf("preface: %v", err)
	}
	c := &rawClient{conn: conn, fr: http2.NewFramer(conn, conn)}
	if err := c.fr.WriteSettings(); err != nil {
		t.Fatalf("settings: %v", err)
	}
	return c
}

// next reads the next frame the server sends, answering its settings and
// its own pings, and returns the ping acks and goaway it reads.
func (c *rawClient) next(t *testing.T) http2.Frame {
	t.Helper()
	for {
		_ = c.conn.SetReadDeadline(time.Now().Add(hostile.Deadline))
		f, err := c.fr.ReadFrame()
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		switch f := f.(type) {
		case *http2.SettingsFrame:
			if !f.IsAck() {
				_ = c.fr.WriteSettingsAck()
			}
		case *http2.PingFrame:
			if !f.IsAck() {
				_ = c.fr.WritePing(true, f.Data)
				continue
			}
			return f
		case *http2.GoAwayFrame:
			return f
		}
	}
}

// startKeepaliveServer serves a server built with opts on a loopback
// listener and returns its address.
func startKeepaliveServer(t *testing.T, opts ...ServerOption) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	quiet, _ := log.NewLogger(log.LogConfig{Driver: "null"})
	s := NewServer(append([]ServerOption{WithListener(lis), WithLogger(quiet), WithEnvironment("testing")}, opts...)...)
	if err := s.StartAsync(); err != nil {
		t.Fatalf("StartAsync: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	return lis.Addr().String()
}

// pingsUntilAnswered sends n pings without a call open and reads until
// every one is acked or the server sends a goaway, which it returns.
func pingsUntilAnswered(t *testing.T, c *rawClient, n int) *http2.GoAwayFrame {
	t.Helper()
	for i := range n {
		if err := c.fr.WritePing(false, [8]byte{byte(i + 1)}); err != nil {
			t.Fatalf("ping: %v", err)
		}
	}
	for acked := 0; acked < n; {
		switch f := c.next(t).(type) {
		case *http2.GoAwayFrame:
			return f
		case *http2.PingFrame:
			acked++
		}
	}
	return nil
}

// WithKeepaliveEnforcementPolicy reaches grpc-go: pings without a call
// open are refused by default (a goaway, too_many_pings) and allowed under
// a policy that permits them.
func TestWithKeepaliveEnforcementPolicy(t *testing.T) {
	const pings = 5
	t.Run("default refuses", func(t *testing.T) {
		c := dialRaw(t, startKeepaliveServer(t))
		ga := pingsUntilAnswered(t, c, pings)
		if ga == nil || ga.ErrCode != http2.ErrCodeEnhanceYourCalm {
			t.Fatalf("goaway = %+v, want ENHANCE_YOUR_CALM for pings without a call", ga)
		}
	})
	t.Run("policy permits", func(t *testing.T) {
		c := dialRaw(t, startKeepaliveServer(t, WithKeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             time.Nanosecond,
			PermitWithoutStream: true,
		})))
		if ga := pingsUntilAnswered(t, c, pings); ga != nil {
			t.Fatalf("goaway = %+v, want every ping acked", ga)
		}
	})
}

// WithKeepaliveParams reaches grpc-go: a connection idle longer than
// MaxConnectionIdle gets a goaway.
func TestWithKeepaliveParams(t *testing.T) {
	c := dialRaw(t, startKeepaliveServer(t, WithKeepaliveParams(keepalive.ServerParameters{
		MaxConnectionIdle: 50 * time.Millisecond,
	})))
	f := c.next(t)
	if _, ok := f.(*http2.GoAwayFrame); !ok {
		t.Fatalf("frame = %T, want a goaway once the connection sat idle", f)
	}
}
