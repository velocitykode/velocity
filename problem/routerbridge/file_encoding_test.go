package routerbridge

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
)

// TestInstall_FailedPrecompressedFileErrorDecodes asserts the error answer
// to a failed c.File of a precompressed asset can be read by a client that
// decodes the body per its Content-Encoding, as every browser does.
//
// The handler serves app.js.gz the usual way: it labels the representation
// gzip, then hands the stored gzip bytes to c.File. When net/http's content
// server refuses the request (416 for a Range the file cannot satisfy, 412
// for a failed If-Match), the error boundary answers with a fresh,
// uncompressed body instead: a problem+json document, the HTML error page
// or the router's plain-text default. That answer must not keep claiming
// the gzip coding the handler set for the file, or the client cannot
// decode it. net/http's own error answer drops Content-Encoding for this
// reason; serveFile hands net/http a cloned header, so that drop never
// reaches the real response.
//
// The pipeline (Install, as the app wires it) and the standalone router
// default are both served through a real server, to a client that asks for
// gzip like a browser and decodes what it receives. The served subtests are
// the control: the asset itself arrives gzip-coded and decodes.
func TestInstall_FailedPrecompressedFileErrorDecodes(t *testing.T) {
	const script = "console.log(\"served precompressed\");\n"
	dir := t.TempDir()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write([]byte(script)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.js.gz"), gz.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	serveAsset := func(c *router.Context) error {
		c.SetHeader("Content-Type", "text/javascript; charset=utf-8")
		c.SetHeader("Content-Encoding", "gzip")
		c.SetHeader("Vary", "Accept-Encoding")
		return c.File("app.js.gz")
	}

	h := problem.NewHandler(problem.WithReporters())
	h.SetDebug(false)
	installed := router.New()
	installed.SetFileRoot(dir)
	Install(installed, WithHandler(func() contract.ErrorHandler { return h }))
	installed.Get("/app.js", serveAsset)

	standalone := router.New()
	standalone.SetFileRoot(dir)
	standalone.Get("/app.js", serveAsset)

	failures := []struct {
		name       string
		header     string
		value      string
		wantStatus int
	}{
		{name: "unsatisfiable range", header: "Range", value: "bytes=999999-", wantStatus: http.StatusRequestedRangeNotSatisfiable},
		{name: "failed precondition", header: "If-Match", value: `"stale"`, wantStatus: http.StatusPreconditionFailed},
	}
	routers := []struct {
		name string
		r    *router.VelocityRouterV2
	}{
		{name: "pipeline", r: installed},
		{name: "standalone", r: standalone},
	}

	for _, rt := range routers {
		srv := httptest.NewServer(rt.r)
		t.Cleanup(srv.Close)

		t.Run(rt.name+"/served", func(t *testing.T) {
			resp, raw := getPrecompressedAsset(t, srv, "*/*", "", "")
			enc := resp.Header.Get("Content-Encoding")
			body, err := decodeByContentEncoding(enc, raw)
			if resp.StatusCode != http.StatusOK || err != nil || string(body) != script {
				t.Fatalf("asset = %d, Content-Encoding %q, decoded %q (err %v); want 200 decoding to %q",
					resp.StatusCode, enc, body, err, script)
			}
		})

		for _, f := range failures {
			for _, accept := range []string{"application/json", "text/html"} {
				t.Run(rt.name+"/"+f.name+"/"+accept, func(t *testing.T) {
					resp, raw := getPrecompressedAsset(t, srv, accept, f.header, f.value)
					if resp.StatusCode != f.wantStatus {
						t.Fatalf("status = %d, want %d", resp.StatusCode, f.wantStatus)
					}
					enc := resp.Header.Get("Content-Encoding")
					body, err := decodeByContentEncoding(enc, raw)
					if err != nil {
						t.Fatalf("%d %s answer labelled Content-Encoding %q cannot be decoded: %v\n\traw body = %.120q",
							resp.StatusCode, resp.Header.Get("Content-Type"), enc, err, raw)
					}
					if enc != "" {
						t.Errorf("error answer carries Content-Encoding %q, want none: the body is the boundary's, not the file's", enc)
					}
					if accept == "application/json" {
						var doc map[string]any
						if err := json.Unmarshal(body, &doc); err != nil || doc["status"] != float64(f.wantStatus) {
							t.Errorf("decoded body = %q (err %v), want a problem document with status %d", body, err, f.wantStatus)
						}
					} else if len(body) == 0 {
						t.Errorf("decoded body is empty, want the %d error answer", f.wantStatus)
					}
				})
			}
		}
	}
}

// TestErrorAnswer_DropsStaleContentEncoding asserts the error answer to a
// handler that labelled its response gzip before failing carries no
// Content-Encoding, on the pipeline and on the standalone router default,
// in every format: the boundary replaces the body, so the coding of the
// failed attempt does not describe it.
func TestErrorAnswer_DropsStaleContentEncoding(t *testing.T) {
	failCompressed := func(c *router.Context) error {
		c.SetHeader("Content-Encoding", "gzip")
		return errors.New("db down")
	}
	h := problem.NewHandler(problem.WithReporters())
	h.SetDebug(false)
	installed := router.New()
	Install(installed, WithHandler(func() contract.ErrorHandler { return h }))
	installed.Get("/app.js", failCompressed)
	standalone := router.New()
	standalone.Get("/app.js", failCompressed)

	for name, r := range map[string]*router.VelocityRouterV2{"pipeline": installed, "standalone": standalone} {
		srv := httptest.NewServer(r)
		t.Cleanup(srv.Close)
		for _, accept := range []string{"application/json", "text/html"} {
			t.Run(name+"/"+accept, func(t *testing.T) {
				resp, raw := getPrecompressedAsset(t, srv, accept, "", "")
				if resp.StatusCode != http.StatusInternalServerError {
					t.Fatalf("status = %d, want 500", resp.StatusCode)
				}
				if enc := resp.Header.Get("Content-Encoding"); enc != "" {
					t.Errorf("error answer carries Content-Encoding %q, want none (body %.80q)", enc, raw)
				}
				if len(raw) == 0 {
					t.Error("error answer body is empty")
				}
			})
		}
	}
}

// getPrecompressedAsset sends GET /app.js the way a browser does: it asks
// for gzip itself, so the transport hands back the body exactly as the
// server coded it, and names the formats it accepts. A non-empty header
// adds that Range or conditional header.
func getPrecompressedAsset(t *testing.T, srv *httptest.Server, accept, header, value string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/app.js", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Encoding", "gzip")
	if header != "" {
		req.Header.Set(header, value)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, raw
}

// decodeByContentEncoding decodes body per the Content-Encoding it was sent
// with, as a browser does: no coding (or identity) passes it through, gzip
// gunzips it. An empty body stays empty whatever the label.
func decodeByContentEncoding(enc string, body []byte) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "", "identity":
		return body, nil
	case "gzip", "x-gzip":
		if len(body) == 0 {
			return body, nil
		}
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		return io.ReadAll(zr)
	default:
		return nil, fmt.Errorf("unsupported Content-Encoding %q", enc)
	}
}
