package velocity

import (
	"database/sql"
	"reflect"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/nilval"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/internal/teardown"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/queue"
)

// ownedFieldCount is the number of owned Services fields (see ownedFields).
const ownedFieldCount = 14

// ownedSet is the value each owned Services field holds at one moment, in
// ownedFields order. A nil or typed-nil field is held as nil.
type ownedSet [ownedFieldCount]any

// ownedFieldNames names the owned Services fields in ownedFields order, as
// error messages and reports name them.
var ownedFieldNames = [ownedFieldCount]string{
	"Log", "Errors", "Crypto", "DB", "Auth", "CSRF", "View",
	"Cache", "Events", "Queue", "Storage", "Scheduler", "Mail", "Notification",
}

// ownedFields returns the value every owned Services field holds now. An
// owned field owns the instance it holds: the instance it holds at
// Shutdown is closed then, and one a module or callback displaced is
// retired at the next boundary (see rebind).
func ownedFields(a *App) ownedSet {
	s := a.Services
	vals := ownedSet{
		s.Log, s.Errors, s.Crypto, s.DB, s.Auth, s.CSRF, s.View,
		s.Cache, s.Events, s.Queue, s.Storage, s.Scheduler, s.Mail, s.Notification,
	}
	for i, v := range vals {
		if nilval.Is(v) {
			vals[i] = nil
		}
	}
	return vals
}

// checkComparable refuses a field holding a value Go cannot compare with
// ==: the boundary tells a displaced instance from the one it holds by
// identity, and comparing such a value would panic.
func checkComparable(vals ownedSet) error {
	for i, v := range vals {
		if v != nil && !reflect.ValueOf(v).Comparable() {
			return errchain.Errorf("velocity: Services.%s holds a %T, which is not comparable; a service value must be comparable (a pointer)", ownedFieldNames[i], v)
		}
	}
	return nil
}

// maxUnwrapDepth bounds how far heldBy follows a value's Unwrap chain.
const maxUnwrapDepth = 8

// unwrapOutcome is what following one value's Unwrap chain found.
type unwrapOutcome int

const (
	unwrapNotFound unwrapOutcome = iota
	unwrapFound
	// unwrapUnknown: the chain could not be followed to its end (an
	// Unwrap panicked, cycled or ran past maxUnwrapDepth), so whether it
	// holds the target is unknown.
	unwrapUnknown
)

// unwrapHolds follows v's Unwrap chain (a method Unwrap taking nothing and
// returning one value) and reports whether it reaches target. v itself is
// not compared. A value Go cannot compare (a value type with a map field,
// say), v or one inside the chain, is never the target and its Unwrap is
// followed like any other; it cannot be recorded for the cycle check, so
// a cycle through such values alone ends at maxUnwrapDepth. A panicking
// Unwrap is recovered and its panic returned.
func unwrapHolds(v, target any) (unwrapOutcome, error) {
	seen := map[any]struct{}{}
	cur := v
	for depth := 0; ; depth++ {
		if cur == nil {
			return unwrapNotFound, nil
		}
		if reflect.ValueOf(cur).Comparable() {
			if _, dup := seen[cur]; dup {
				return unwrapUnknown, nil
			}
			seen[cur] = struct{}{}
		}
		next, ok, err := callUnwrap(cur)
		if err != nil {
			return unwrapUnknown, err
		}
		if !ok {
			return unwrapNotFound, nil
		}
		if next == nil {
			return unwrapNotFound, nil
		}
		if depth >= maxUnwrapDepth {
			return unwrapUnknown, nil
		}
		if teardown.SameInstance(next, target) {
			return unwrapFound, nil
		}
		cur = next
	}
}

// callUnwrap calls v's Unwrap method when it has one of the shape
// Unwrap() T, contained: a panic is returned as a *panicerr.Error. ok is
// false when v has no such method. A typed-nil result is nil.
func callUnwrap(v any) (next any, ok bool, err error) {
	m := reflect.ValueOf(v).MethodByName("Unwrap")
	if !m.IsValid() {
		return nil, false, nil
	}
	mt := m.Type()
	if mt.NumIn() != 0 || mt.NumOut() != 1 {
		return nil, false, nil
	}
	defer func() {
		if r := recover(); r != nil {
			next, ok, err = nil, true, panicerr.FromRecovered(r)
		}
	}()
	out := m.Call(nil)[0]
	if !out.IsValid() || (out.Kind() == reflect.Interface && out.IsNil()) {
		return nil, true, nil
	}
	res := out.Interface()
	if nilval.Is(res) {
		return nil, true, nil
	}
	return res, true, nil
}

// displacedSince returns, once each, the instances a field held at prev
// and holds no more at cur, and that nothing at cur still holds: another
// owned field, a registered component or hook, or the Unwrap chain of any
// of them. A chain that cannot be followed to its end (see unwrapHolds)
// counts as holding the instance, the safe direction: it is kept rather
// than closed under a wrapper still using it, and the failure is returned
// in unknown, once per instance, for the caller to report.
func displacedSince(a *App, prev, cur ownedSet) (displaced []any, unknown []error) {
	var holders []any
	for _, v := range cur {
		if v != nil {
			holders = append(holders, v)
		}
	}
	a.Services.RangeComponents(func(_ app.ComponentKey, v any, hooks []any) bool {
		holders = append(holders, v)
		holders = append(holders, hooks...)
		return true
	})
	for i, old := range prev {
		if old == nil || teardown.SameInstance(old, cur[i]) {
			continue
		}
		if containsIdentity(displaced, old) {
			continue
		}
		held := false
		for _, h := range holders {
			if nilval.Is(h) {
				continue
			}
			// A holder that cannot be compared (a registered value type)
			// is never the instance itself, but its chain may reach it.
			if teardown.SameInstance(h, old) {
				held = true
				break
			}
			outcome, err := unwrapHolds(h, old)
			if outcome == unwrapFound {
				held = true
				break
			}
			if outcome == unwrapUnknown {
				held = true
				if err == nil {
					err = errchain.Errorf("velocity: the Unwrap chain of %T could not be followed (a cycle or deeper than %d)", h, maxUnwrapDepth)
				}
				unknown = append(unknown, errchain.Errorf("velocity: Services.%s: displaced %T kept, not closed: %w", ownedFieldNames[i], old, err))
				break
			}
		}
		if !held {
			displaced = append(displaced, old)
		}
	}
	return displaced, unknown
}

func containsIdentity(list []any, v any) bool {
	for _, x := range list {
		if teardown.SameInstance(x, v) {
			return true
		}
	}
	return false
}

// Owned field indexes, in ownedFields order.
const (
	fieldLog = iota
	fieldErrors
	fieldCrypto
	fieldDB
	fieldAuth
	fieldCSRF
	fieldView
	fieldCache
	fieldEvents
	fieldQueue
	fieldStorage
	fieldScheduler
	fieldMail
	fieldNotification
)

// boundConsumer is one framework-installed binding to an owned field: a
// value New handed a collaborator once (an encryptor, a cache store, a
// mailer, a database handle) that must follow the field when a module or
// callback replaces it. rebind runs prepare for every consumer whose field
// changed since the previous boundary; prepare checks the replacement can
// serve the consumer and returns the commit that re-binds it, or an error
// naming the field and the capability it lacks. Nothing commits unless
// every prepare succeeded.
//
// A consumer re-binds only the binding the framework installed: one a
// module replaced with its own collaborator is kept.
type boundConsumer struct {
	name    string
	fields  []int
	prepare func(a *App, prev, cur ownedSet) (commit func(), err error)
}

// boundConsumers lists every binding New captures once from an owned
// field. It is the enumerator of the class: a site that hands an owned
// field's value to a collaborator at boot has a row here, and the wiring
// conformance test swaps every owned field and checks each row follows.
// Events, Errors and Log are re-bound at every boundary by
// wireInstanceEvents, whatever changed. The queued-listener integration
// New initializes captures no queue driver (it binds one only to a
// dispatcher it is given, and New gives none), so it has no row.
var boundConsumers = []boundConsumer{
	{name: "orm default manager", fields: []int{fieldDB}, prepare: rebindORMDefault},
	{name: "session, cookie and queue payload encryptor", fields: []int{fieldCrypto}, prepare: rebindEncryptor},
	{name: "server session store cache", fields: []int{fieldCache}, prepare: rebindSessionCache},
	{name: "notification mail channel mailer", fields: []int{fieldMail}, prepare: rebindMailer},
	{name: "notification database channel", fields: []int{fieldDB}, prepare: rebindNotificationDB},
	{name: "login throttler", fields: []int{fieldCache}, prepare: rebindLoginThrottler},
	{name: "scheduler locker", fields: []int{fieldCache, fieldScheduler}, prepare: rebindSchedulerLocker},
	{name: "batch callback queue", fields: []int{fieldQueue}, prepare: rebindBatchCallbackQueue},
	{name: "csrf token rotator", fields: []int{fieldCSRF, fieldAuth}, prepare: rebindCSRFTokenRotator},
	{name: "error page renderer", fields: []int{fieldErrors}, prepare: rebindErrorPageRenderer},
}

// changed reports whether any of fields holds a different value at cur
// than at prev.
func changed(prev, cur ownedSet, fields []int) bool {
	for _, f := range fields {
		if (prev[f] != nil || cur[f] != nil) && !teardown.SameInstance(prev[f], cur[f]) {
			return true
		}
	}
	return false
}

// bindConsumers prepares every consumer whose field changed, then commits
// them all, or commits none and returns the first preparation error.
func bindConsumers(a *App, prev, cur ownedSet) error {
	var commits []func()
	for _, c := range boundConsumers {
		if !changed(prev, cur, c.fields) {
			continue
		}
		var commit func()
		err := teardown.Step(func() (err error) {
			commit, err = c.prepare(a, prev, cur)
			return err
		})
		if panicerr.AsTyped(err) != nil {
			return errchain.Errorf("velocity: re-binding the %s to the replaced service: %w", c.name, err)
		}
		if err != nil {
			return err
		}
		if commit != nil {
			commits = append(commits, commit)
		}
	}
	for _, commit := range commits {
		commit()
	}
	return nil
}

// rebindORMDefault moves the ORM's process-wide default manager to the
// manager Services.DB holds now, when it is still the one the field held
// before (the default New installed or an earlier boundary moved). A
// replacement that is not an *orm.Manager, or none, clears it, as New
// leaves it without a database.
func rebindORMDefault(_ *App, prev, cur ownedSet) (func(), error) {
	old, _ := prev[fieldDB].(*orm.Manager)
	if old == nil || orm.Default() != old {
		return nil, nil
	}
	next, _ := cur[fieldDB].(*orm.Manager)
	return func() {
		if next == nil {
			orm.ResetDefault()
			return
		}
		orm.SetDefault(next)
	}, nil
}

// notificationDBChannel is the notification database channel's surface
// rebind uses: the database it holds and the setter.
type notificationDBChannel interface {
	DB() (*sql.DB, string)
	SetDB(db *sql.DB, driver ...string)
}

// rebindNotificationDB points the notification database channel New built
// at the database Services.DB holds now, with its driver name, while the
// channel still holds the *sql.DB the framework gave it: one a module set
// on the channel itself is kept. With no database the channel holds none,
// and its sends fail as they do without one.
func rebindNotificationDB(a *App, _, cur ownedSet) (func(), error) {
	ch := a.dbChannel
	if ch == nil {
		return nil, nil
	}
	if held, _ := ch.DB(); held != a.dbChannelDB {
		return nil, nil
	}
	var db *sql.DB
	driver := ""
	if next, _ := cur[fieldDB].(contract.Database); next != nil {
		db, driver = next.DB(), next.DriverName()
	}
	return func() {
		if driver == "" {
			ch.SetDB(db)
		} else {
			ch.SetDB(db, driver)
		}
		a.dbChannelDB = db
	}, nil
}

// rebindBatchCallbackQueue points the batch package's callback queue at
// the driver Services.Queue holds now. The holder is process-wide and has
// no reader, so a callback queue a module installed itself is replaced
// too.
func rebindBatchCallbackQueue(_ *App, _, cur ownedSet) (func(), error) {
	next, _ := cur[fieldQueue].(queue.Driver)
	return func() { queue.SetBatchCallbackQueue(next, "default") }, nil
}

// rebindCSRFTokenRotator re-installs the auth manager's CSRF token
// rotator (see installCSRFTokenRotator).
func rebindCSRFTokenRotator(a *App, _, _ ownedSet) (func(), error) {
	return func() { installCSRFTokenRotator(a) }, nil
}

// rebindErrorPageRenderer hands an error handler a module installed the
// app's view-resolving error page (see installErrorPageRenderer).
func rebindErrorPageRenderer(a *App, _, _ ownedSet) (func(), error) {
	return func() { installErrorPageRenderer(a) }, nil
}

// rebind is the one wiring pass, run at every lifecycle boundary: in New
// before and after the WithModules lifecycle, and in bootstrap after the
// chain modules' Start, after the Middleware, Routes and Events callbacks
// and after the Errors step. A module or callback may have replaced any
// owned Services field since the previous boundary; rebind
//
//  1. refuses a field holding a non-comparable value, naming the field;
//  2. re-binds every framework-installed consumer of a replaced field
//     (boundConsumers) to the instance the field holds now, or, when the
//     replacement cannot serve one, returns an error naming the field and
//     the capability and changes nothing;
//  3. re-binds the event dispatcher, the error handler and the logger at
//     every boundary (wireInstanceEvents) and points the session save seam
//     at the current default session scheme;
//  4. retires each displaced instance nothing still holds (see
//     displacedSince), contained and tracked: closed once, awaited for a
//     bounded grace here and in full by Shutdown.
//
// A field replaced and replaced again between two boundaries (A, B, C)
// leaves B to the module that installed it; one put back (A, B, A)
// retires nothing. Services are published by the last boundary: a field
// replaced after bootstrap is not re-bound or retired.
func rebind(a *App) error {
	cur := ownedFields(a)
	if err := checkComparable(cur); err != nil {
		return err
	}
	if err := a.retiredReassigned(cur); err != nil {
		return err
	}
	prev := a.bound
	if !a.boundSet {
		prev = cur
	}
	if err := bindConsumers(a, prev, cur); err != nil {
		return err
	}
	wireInstanceEvents(a)
	refreshSessionScheme(a)
	a.bound, a.boundSet = cur, true
	a.retireDisplaced(prev, cur, retireGrace)
	return nil
}
