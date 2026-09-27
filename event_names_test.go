package velocity

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/bond"
	"github.com/velocitykode/velocity/bus"
	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto/drivers"
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/httpclient"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/notification"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/queue"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/scheduler"
)

// frameworkEvent is one framework event type and the subsystem its name
// must start with.
type frameworkEvent struct {
	event     contract.Event
	subsystem string
}

// frameworkEvents holds one value of every exported framework event type.
// TestFrameworkEventTable_CoversEveryEventType keeps it complete.
var frameworkEvents = []frameworkEvent{
	{auth.PasswordNeedsRehashEvent{}, "auth"},

	{&bond.SSRRenderFailed{}, "bond"},

	{&bus.CommandDispatching{}, "bus"},
	{&bus.CommandCompleted{}, "bus"},
	{&bus.CommandFailed{}, "bus"},
	{&bus.CommandQueued{}, "bus"},

	{&cache.CacheHit{}, "cache"},
	{&cache.CacheMiss{}, "cache"},
	{&cache.CacheWritten{}, "cache"},
	{&cache.CacheForgotten{}, "cache"},
	{&cache.CacheOperationFailed{}, "cache"},

	{&drivers.LegacyDecryptEvent{}, "crypto"},

	{&csrf.SessionMissing{}, "csrf"},

	{&events.AsyncFailed{}, "events"},

	{&grpcevents.RequestStarted{}, "grpc"},
	{&grpcevents.RequestCompleted{}, "grpc"},
	{&grpcevents.RequestFailed{}, "grpc"},
	{&grpcevents.StreamStarted{}, "grpc"},
	{&grpcevents.StreamCompleted{}, "grpc"},
	{&grpcevents.StreamFailed{}, "grpc"},
	{&grpcevents.ServerStarted{}, "grpc"},
	{&grpcevents.ServerStopped{}, "grpc"},
	{&grpcevents.GatewayStarted{}, "grpc"},
	{&grpcevents.GatewayStopped{}, "grpc"},
	{&grpcevents.PanicRecovered{}, "grpc"},
	{&grpcevents.AuthFailed{}, "grpc"},

	{&httpclient.RequestSent{}, "httpclient"},
	{&httpclient.RequestFailed{}, "httpclient"},

	{&mail.MailSent{}, "mail"},
	{&mail.MailFailed{}, "mail"},

	{&notification.NotificationSent{}, "notification"},
	{&notification.NotificationFailed{}, "notification"},

	{&orm.QueryExecuted{}, "orm"},
	{&orm.QueryFailed{}, "orm"},
	{&orm.TxRecover{}, "orm"},
	{&orm.TransactionExecuted{}, "orm"},

	{&queue.JobQueued{}, "queue"},
	{&queue.JobProcessing{}, "queue"},
	{&queue.JobProcessed{}, "queue"},
	{&queue.JobFailed{}, "queue"},
	{&queue.JobRetrying{}, "queue"},
	{&queue.BatchCreated{}, "queue"},
	{&queue.BatchJobCompleted{}, "queue"},
	{&queue.BatchJobFailed{}, "queue"},
	{&queue.BatchCompleted{}, "queue"},
	{&queue.BatchCancelled{}, "queue"},

	{&router.RequestStarted{}, "router"},
	{&router.RequestRouted{}, "router"},
	{&router.RequestHandled{}, "router"},
	{&router.RequestFailed{}, "router"},

	{&scheduler.ScheduledTaskStarting{}, "scheduler"},
	{&scheduler.ScheduledTaskFinished{}, "scheduler"},
	{&scheduler.ScheduledTaskFailed{}, "scheduler"},
}

// notFrameworkEvents are exported types that implement the named-event
// facet without being framework events, with the reason.
var notFrameworkEvents = map[string]string{
	"github.com/velocitykode/velocity/events.BaseEvent":          "carries an application-supplied name",
	"github.com/velocitykode/velocity/events.BaseStoppableEvent": "carries an application-supplied name",
	"github.com/velocitykode/velocity/events.ModelEvent":         "names an application model's lifecycle event",
	"github.com/velocitykode/velocity/storage/testing.FakeFile":  "a file name, not an event",
}

// eventNameGrammar is the shape of every framework event name: dot-separated
// segments of lowercase ASCII letters, at least two of them.
var eventNameGrammar = regexp.MustCompile(`^[a-z]+(\.[a-z]+)+$`)

// eventNameVerbs is the closed set of past-tense verbs a framework event
// name may end in. A run of work uses started, completed and failed for its
// stages and nothing else; the rest name a single occurrence.
var eventNameVerbs = map[string]bool{
	"started":   true,
	"completed": true,
	"failed":    true,

	"cancelled": true,
	"created":   true,
	"decrypted": true,
	"forgotten": true,
	"hit":       true,
	"missed":    true,
	"needed":    true,
	"queued":    true,
	"recovered": true,
	"retried":   true,
	"routed":    true,
	"stopped":   true,
	"written":   true,
}

// eventNameViolation returns why name breaks the framework event name rule
// for subsystem, or "" when it follows it.
func eventNameViolation(name, subsystem string) string {
	if !eventNameGrammar.MatchString(name) {
		return "is not dot-separated lowercase segments"
	}
	segments := strings.Split(name, ".")
	if segments[0] != subsystem {
		return "does not start with its subsystem " + strconv.Quote(subsystem)
	}
	if verb := segments[len(segments)-1]; !eventNameVerbs[verb] {
		return "ends in " + strconv.Quote(verb) + ", not one of the framework's past-tense verbs"
	}
	return ""
}

// eventTypeKey names the Go type of an event value: import path, a dot and
// the type name, pointers dereferenced.
func eventTypeKey(event any) string {
	t := reflect.TypeOf(event)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.PkgPath() + "." + t.Name()
}

// TestFrameworkEventNames_FollowTheRule checks every framework event name
// against the rule: subsystem prefix, dot-separated lowercase segments, one
// past-tense verb from the closed set, and no two event types sharing a name.
func TestFrameworkEventNames_FollowTheRule(t *testing.T) {
	owner := map[string]string{}
	for _, fe := range frameworkEvents {
		name := fe.event.Name()
		key := eventTypeKey(fe.event)
		if why := eventNameViolation(name, fe.subsystem); why != "" {
			t.Errorf("%s: name %q %s", key, name, why)
		}
		if prev, dup := owner[name]; dup {
			t.Errorf("%s and %s share the name %q", prev, key, name)
		}
		owner[name] = key
	}
}

// TestEventNameViolation_RejectsEachBreach pins the rule check itself.
func TestEventNameViolation_RejectsEachBreach(t *testing.T) {
	tests := []struct {
		name, subsystem string
		ok              bool
	}{
		{"queue.job.completed", "queue", true},
		{"cache.hit", "cache", true},
		{"queue.job.processed", "queue", false},
		{"job.completed", "queue", false},
		{"orm.tx_recover", "orm", false},
		{"csrf.sessionMissing", "csrf", false},
		{"queue", "queue", false},
		{"queue.job.", "queue", false},
		{"scheduler.task.starting", "scheduler", false},
	}
	for _, tt := range tests {
		if got := eventNameViolation(tt.name, tt.subsystem) == ""; got != tt.ok {
			t.Errorf("eventNameViolation(%q, %q) accepts = %v, want %v", tt.name, tt.subsystem, got, tt.ok)
		}
	}
}

// eventSourceTree is the framework's non-test source parsed for the event
// guards.
type eventSourceTree struct {
	files []eventSourceFile
}

// eventSourceFile is one parsed non-test framework file.
type eventSourceFile struct {
	path    string // slash-separated, relative to the module root
	file    *ast.File
	imports map[string]string // local name -> import path
}

func parseEventSourceTree(t *testing.T) *eventSourceTree {
	t.Helper()
	tree := &eventSourceTree{}
	walkNonTestGo(t, ".", func(p string, src []byte) {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		imports := map[string]string{}
		for _, imp := range f.Imports {
			ip, _ := strconv.Unquote(imp.Path.Value)
			local := path.Base(ip)
			if imp.Name != nil {
				local = imp.Name.Name
			}
			imports[local] = ip
		}
		tree.files = append(tree.files, eventSourceFile{path: p, file: f, imports: imports})
	})
	return tree
}

// importPath returns the import path of the package that holds file.
func (f eventSourceFile) importPath() string {
	dir := path.Dir(f.path)
	if dir == "." {
		return "github.com/velocitykode/velocity"
	}
	return "github.com/velocitykode/velocity/" + dir
}

// isNameMethod reports whether fn is a `Name() string` method and returns
// its receiver's type name.
func isNameMethod(fn *ast.FuncDecl) (string, bool) {
	if fn.Recv == nil || len(fn.Recv.List) != 1 || fn.Name.Name != "Name" {
		return "", false
	}
	if fn.Type.Params.NumFields() != 0 || fn.Type.Results.NumFields() != 1 {
		return "", false
	}
	if id, ok := fn.Type.Results.List[0].Type.(*ast.Ident); !ok || id.Name != "string" {
		return "", false
	}
	recv := fn.Recv.List[0].Type
	if star, ok := recv.(*ast.StarExpr); ok {
		recv = star.X
	}
	id, ok := recv.(*ast.Ident)
	if !ok {
		return "", false
	}
	return id.Name, true
}

// namedEventTypes returns every exported type in the framework that has a
// `Name() string` method, declared or promoted from an embedded field.
func (tree *eventSourceTree) namedEventTypes() map[string]bool {
	named := map[string]bool{}
	for _, f := range tree.files {
		for _, decl := range f.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if recv, ok := isNameMethod(fn); ok && ast.IsExported(recv) {
				named[f.importPath()+"."+recv] = true
			}
		}
	}
	// Promote through embedding until nothing changes.
	for changed := true; changed; {
		changed = false
		for _, f := range tree.files {
			ast.Inspect(f.file, func(n ast.Node) bool {
				spec, ok := n.(*ast.TypeSpec)
				if !ok || !ast.IsExported(spec.Name.Name) {
					return true
				}
				st, ok := spec.Type.(*ast.StructType)
				if !ok {
					return true
				}
				key := f.importPath() + "." + spec.Name.Name
				if named[key] {
					return true
				}
				for _, field := range st.Fields.List {
					if len(field.Names) != 0 {
						continue
					}
					if embedded := f.embeddedKey(field.Type); embedded != "" && named[embedded] {
						named[key] = true
						changed = true
						return true
					}
				}
				return true
			})
		}
	}
	return named
}

// embeddedKey resolves an embedded field's type to its import path and name.
func (f eventSourceFile) embeddedKey(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	switch e := expr.(type) {
	case *ast.Ident:
		return f.importPath() + "." + e.Name
	case *ast.SelectorExpr:
		if pkg, ok := e.X.(*ast.Ident); ok {
			return f.imports[pkg.Name] + "." + e.Sel.Name
		}
	}
	return ""
}

// TestFrameworkEventTable_CoversEveryEventType keeps frameworkEvents
// complete: every exported type with a Name method is either in the table
// or listed, with a reason, as not a framework event.
func TestFrameworkEventTable_CoversEveryEventType(t *testing.T) {
	inTable := map[string]bool{}
	for _, fe := range frameworkEvents {
		inTable[eventTypeKey(fe.event)] = true
	}
	var missing, stale []string
	named := parseEventSourceTree(t).namedEventTypes()
	for key := range named {
		if !inTable[key] && notFrameworkEvents[key] == "" {
			missing = append(missing, key)
		}
	}
	for key := range inTable {
		if !named[key] {
			stale = append(stale, key)
		}
	}
	for key := range notFrameworkEvents {
		if !named[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	for _, key := range missing {
		t.Errorf("%s has a Name method but is not in frameworkEvents (or notFrameworkEvents with a reason)", key)
	}
	for _, key := range stale {
		t.Errorf("%s is listed but no longer has a Name method", key)
	}
}

// TestFrameworkPackages_DeclareNoOwnEventInterface requires the named-event
// facet to be declared once, in contract: no other package declares an
// interface whose only method is `Name() string`.
func TestFrameworkPackages_DeclareNoOwnEventInterface(t *testing.T) {
	var offenders []string
	for _, f := range parseEventSourceTree(t).files {
		if strings.HasPrefix(f.path, "contract/") {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			it, ok := spec.Type.(*ast.InterfaceType)
			if !ok || len(it.Methods.List) != 1 {
				return true
			}
			m := it.Methods.List[0]
			ft, ok := m.Type.(*ast.FuncType)
			if !ok || len(m.Names) != 1 || m.Names[0].Name != "Name" || ft.Params.NumFields() != 0 || ft.Results.NumFields() != 1 {
				return true
			}
			if id, ok := ft.Results.List[0].Type.(*ast.Ident); ok && id.Name == "string" {
				offenders = append(offenders, f.path+": "+spec.Name.Name)
			}
			return true
		})
	}
	sort.Strings(offenders)
	for _, o := range offenders {
		t.Errorf("%s declares its own Name() interface; use contract.Event", o)
	}
}

// typedListener records the Go type of every event it receives.
type typedListener struct {
	mu   sync.Mutex
	seen []string
}

func (l *typedListener) Handle(_ context.Context, event any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = append(l.seen, eventTypeKey(event))
	return nil
}

func (l *typedListener) Async() bool { return false }

func (l *typedListener) received() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.seen)
}

// TestFailureFacet_ReceivesEveryFrameworkFailureEvent dispatches every
// framework event to a listener subscribed to the failure facet and a
// listener subscribed to one event type: the first receives exactly the
// events implementing contract.FailureEvent, the second exactly its type.
func TestFailureFacet_ReceivesEveryFrameworkFailureEvent(t *testing.T) {
	d := events.NewDispatcher()
	failures := &typedListener{}
	jobFailures := &typedListener{}
	d.Listen(events.OfType[contract.FailureEvent](), failures)
	d.Listen(events.OfType[*queue.JobFailed](), jobFailures)

	var want []string
	for _, fe := range frameworkEvents {
		if _, ok := fe.event.(contract.FailureEvent); ok {
			want = append(want, eventTypeKey(fe.event))
		}
		if err := d.Dispatch(context.Background(), fe.event); err != nil {
			t.Fatalf("Dispatch(%s): %v", eventTypeKey(fe.event), err)
		}
	}

	if len(want) == 0 {
		t.Fatal("no framework event implements contract.FailureEvent")
	}
	if got := failures.received(); !slices.Equal(got, want) {
		t.Errorf("failure facet received %v, want %v", got, want)
	}
	if got, want := jobFailures.received(), []string{"github.com/velocitykode/velocity/queue.JobFailed"}; !slices.Equal(got, want) {
		t.Errorf("OfType[*queue.JobFailed] received %v, want %v", got, want)
	}
}
