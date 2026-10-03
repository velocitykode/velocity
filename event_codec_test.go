package velocity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/schemes"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/orm"
)

// capturedEvents collects the events a dispatcher is handed.
type capturedEvents struct {
	mu  sync.Mutex
	evs []any
}

func (c *capturedEvents) dispatch(_ context.Context, event any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evs = append(c.evs, event)
	return nil
}

// first returns the first captured event of want's type.
func (c *capturedEvents) first(t *testing.T, want reflect.Type) any {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.evs {
		if reflect.TypeOf(e) == want {
			return e
		}
	}
	t.Fatalf("no %v dispatched among %d events", want, len(c.evs))
	return nil
}

// queueRoundTrip does to event what the queue does to an event handed to a
// queued listener: marshal it, then unmarshal the bytes into a fresh value
// of the event's own type, as the registered event factory provides.
func queueRoundTrip(t *testing.T, event any) any {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal %T: %v", event, err)
	}
	typ := reflect.TypeOf(event)
	fresh := reflect.New(typ)
	if typ.Kind() == reflect.Pointer {
		fresh.Elem().Set(reflect.New(typ.Elem()))
		if err := json.Unmarshal(data, fresh.Elem().Interface()); err != nil {
			t.Fatalf("unmarshal %T: %v", event, err)
		}
	} else if err := json.Unmarshal(data, fresh.Interface()); err != nil {
		t.Fatalf("unmarshal %T: %v", event, err)
	}
	return fresh.Elem().Interface()
}

// payload returns the event's fields other than its envelope (EventMeta:
// its context never crosses, its time loses the monotonic reading), as a
// map from field name to value.
func payload(event any) map[string]any {
	v := reflect.ValueOf(event)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	out := map[string]any{}
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		if !f.IsExported() || f.Name == "EventMeta" {
			continue
		}
		out[f.Name] = v.Field(i).Interface()
	}
	return out
}

// codecUser is a user whose identifier is an integer primary key, the
// default shape, which JSON would turn into a float64.
type codecUser struct{}

func (codecUser) GetAuthIdentifier() interface{} { return uint(7) }
func (codecUser) GetAuthPassword() string        { return "stub:correct" }
func (codecUser) GetRememberToken() string       { return "" }
func (codecUser) SetRememberToken(string)        {}

// codecStore finds codecUser for any credentials.
type codecStore struct{}

func (codecStore) FindByIDCtx(context.Context, interface{}) (contract.Authenticatable, error) {
	return codecUser{}, nil
}
func (codecStore) FindByID(interface{}) (contract.Authenticatable, error) { return codecUser{}, nil }
func (codecStore) FindByCredentialsCtx(context.Context, map[string]interface{}) (contract.Authenticatable, error) {
	return codecUser{}, nil
}
func (codecStore) FindByCredentials(map[string]interface{}) (contract.Authenticatable, error) {
	return codecUser{}, nil
}
func (codecStore) ValidateCredentials(contract.Authenticatable, map[string]interface{}) bool {
	return true
}
func (codecStore) UpdateRememberTokenCtx(context.Context, contract.Authenticatable, string) error {
	return nil
}
func (codecStore) UpdateRememberToken(contract.Authenticatable, string) error { return nil }

// codecHasher accepts every password and always asks for a rehash.
type codecHasher struct{}

func (codecHasher) Hash(p string) (string, error) { return "stub:" + p, nil }
func (codecHasher) Verify(string, string) bool    { return true }
func (codecHasher) NeedsRehash(string) bool       { return true }

// codecPanic is a panic value of a user type: JSON would turn it into a map.
type codecPanic struct{ Code int }

// capturingListener hands every event it handles to c.
type capturingListener struct{ c *capturedEvents }

func (l capturingListener) Handle(ctx context.Context, event interface{}) error {
	return l.c.dispatch(ctx, event)
}
func (capturingListener) Async() bool { return false }

// codecModel is an application model.
type codecModel struct{ ID int }

// Every framework event a queued listener can receive comes back from the
// queue codec equal to what a synchronous listener sees, for values of the
// shapes an app produces: an integer user ID, a panic value of a user type,
// an integer query binding, a model pointer.
func TestFrameworkEvents_SurviveTheQueueCodec(t *testing.T) {
	cases := []struct {
		name    string
		want    reflect.Type
		produce func(t *testing.T, c *capturedEvents)
	}{
		{"auth.password.rehash.needed", reflect.TypeOf(auth.PasswordNeedsRehashEvent{}), func(t *testing.T, c *capturedEvents) {
			enc, err := crypto.NewEncryptor(crypto.Config{Key: strings.Repeat("k", 32), Cipher: "AES-256-GCM"})
			if err != nil {
				t.Fatalf("NewEncryptor: %v", err)
			}
			scheme, err := schemes.NewSessionScheme(codecStore{}, auth.SessionConfig{Name: "vel_session", IdleLifetime: 60, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode}, enc)
			if err != nil {
				t.Fatalf("NewSessionScheme: %v", err)
			}
			scheme.SetHasher(codecHasher{})
			scheme.SetAttemptFloor(-1)
			scheme.SetEventDispatcher(c.dispatch)
			r := httptest.NewRequest(http.MethodPost, "/login", nil)
			if ok, err := scheme.Attempt(httptest.NewRecorder(), r, map[string]interface{}{"email": "a@example.com", "password": "correct"}); !ok || err != nil {
				t.Fatalf("Attempt = %v, %v", ok, err)
			}
		}},
		{"grpc.panic.recovered", reflect.TypeOf(&grpcevents.PanicRecovered{}), func(t *testing.T, c *capturedEvents) {
			pair := interceptors.CallLifecycle(interceptors.WithEventDispatcher(c.dispatch))
			_, _ = pair.Unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/codec.Service/Boom"}, func(context.Context, any) (any, error) {
				panic(codecPanic{Code: 7})
			})
		}},
		{"orm.query.completed", reflect.TypeOf(&orm.QueryExecuted{}), func(t *testing.T, c *capturedEvents) {
			m, err := orm.NewManager(orm.ManagerConfig{Driver: "sqlite", Database: ":memory:"})
			if err != nil {
				t.Fatalf("NewManager: %v", err)
			}
			t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
			m.SetEventDispatcher(c.dispatch)
			var n int64
			if err := m.DB().QueryRowContext(context.Background(), "SELECT ?", int64(7)).Scan(&n); err != nil {
				t.Fatalf("query: %v", err)
			}
			if err := m.FlushQueryEvents(context.Background()); err != nil {
				t.Fatalf("FlushQueryEvents: %v", err)
			}
		}},
		{"codecmodel.created", reflect.TypeOf(&events.ModelEvent{}), func(t *testing.T, c *capturedEvents) {
			d := events.NewObservableDispatcher()
			d.Listen("codecmodel.created", capturingListener{c})
			if err := d.FireModelEvent(context.Background(), "created", &codecModel{ID: 7}); err != nil {
				t.Fatalf("FireModelEvent: %v", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &capturedEvents{}
			tc.produce(t, c)
			seen := c.first(t, tc.want)
			async := queueRoundTrip(t, seen)
			if got, want := payload(async), payload(seen); !reflect.DeepEqual(got, want) {
				t.Errorf("after the queue codec the event reads\n%#v\nwhere a synchronous listener saw\n%#v", got, want)
			}
		})
	}
}
