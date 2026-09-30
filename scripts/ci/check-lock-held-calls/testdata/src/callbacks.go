package lockheld

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// Callback calls: json, gob and io calls run methods of the values they
// are given.

type CB struct {
	mu     sync.Mutex
	logger Logger
}

// plain has only fields json encodes without calling module code: a stdlib
// marshaler (time.Time, json.RawMessage), an interface json skips, and an
// unexported one it cannot see.
type plain struct {
	At     time.Time
	Raw    json.RawMessage
	Skip   any `json:"-"`
	hidden any
}

// open has an exported interface field: its value can be any code.
type open struct {
	Value any
}

// loud marshals through module code that logs.
type loud struct{ cb *CB }

func (l loud) MarshalJSON() ([]byte, error) {
	l.cb.logger.Warn("marshal")
	return []byte("{}"), nil
}

// quiet marshals through module code that calls nothing.
type quiet struct{}

func (quiet) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// wrapper holds a loud value in a slice inside a map.
type wrapper struct {
	Items map[string][]loud
}

func (c *CB) Encode(v any, p plain, o open, w wrapper) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = json.Marshal(v) // want callback
	_, _ = json.Marshal(p)
	_, _ = json.Marshal(&p)
	_, _ = json.Marshal(o)                // want callback
	_, _ = json.Marshal(loud{c})          // want reach
	_, _ = json.Marshal(w)                // want reach
	_, _ = json.Marshal(quiet{})          // quiet's MarshalJSON reaches no user code
	_, _ = json.MarshalIndent(v, "", " ") // want callback
}

func Generic[T any](mu *sync.Mutex, v T) {
	mu.Lock()
	defer mu.Unlock()
	_, _ = json.Marshal(v) // want callback
}

func (c *CB) Decode(data []byte, into any, p *plain) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var fresh any
	_ = json.Unmarshal(data, &fresh) // a zero variable holds no user value
	var reused any
	reused = into
	_ = json.Unmarshal(data, &reused) // want callback
	_ = json.Unmarshal(data, into)    // want callback
	_ = json.Unmarshal(data, p)
	var o open
	_ = json.Unmarshal(data, &o)
	_ = json.NewDecoder(bytes.NewReader(data)).Decode(&o) // want callback
}

func (c *CB) Copy(dst io.Writer, src io.Reader, f, g *os.File) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = io.Copy(dst, src) // want callback
	_, _ = io.Copy(f, g)
	_, _ = io.ReadAll(src) // want callback
	_, _ = io.ReadAll(bytes.NewReader(nil))
	r := io.LimitReader(src, 10) // building a reader calls nothing
	_ = r
	lr := &io.LimitedReader{R: src, N: 1}
	_, _ = lr.Read(nil) // want callback
}

func (c *CB) Gob(v any) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = enc.Encode(v) // want callback
}

// encodeValue reaches user code through json.Marshal: a caller holding a
// lock reaches it too.
func encodeValue(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func (c *CB) ThroughHelper(v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = encodeValue(v) // want reach
}

func (c *CB) Unlocked(v any, dst io.Writer, src io.Reader) {
	_, _ = json.Marshal(v)
	_, _ = io.Copy(dst, src)
}
