package velocity

import (
	"context"
	"sync"
	"weak"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/trace"
)

// The async and trace packages hold process-wide state an app installs:
// the async panic hook, which reports a panic recovered in a background
// goroutine to the app's error handler, and the package loggers async and
// trace write their own lines to. With several apps in one process the
// newest live app owns that state: its values are the installed ones.
//
// Every app that wires the state keeps one entry on a stack, oldest first.
// An app's later wiring (a lifecycle boundary after a module swapped
// Services.Log or Services.Errors) updates its own entry, and reaches the
// packages only while the app owns the state: an older app never takes it
// back from a newer one. An app's Shutdown, or the cleanup of its failed
// New, removes its entry; when that app owned the state, the previous live
// app's values are installed again, and with no app left the packages go
// back to their defaults (no hook, the standalone fallback logger). An
// app never clears or replaces another app's installation.
//
// Known limit: the hook and loggers are per process, not per goroutine. A
// panic recovered in a goroutine an older app started, while a newer app
// lives, is reported to the newer app's error handler. An app never shut
// down counts as live: when a newer app shuts down, the state returns to
// it.
var (
	packageStateMu sync.Mutex
	packageStack   []*packageInstall
)

// packageInstall is one app's values for the process-wide package state.
// The logger and the hook are recorded by two steps of the same wiring
// boundary; a nil field is installed as nil (the package default).
//
// The app is held weakly, for identity only: an app dropped without
// Shutdown is not kept alive by its entry (its entry, holding only its
// logger and hook, stays until the process ends, still counted live).
type packageInstall struct {
	app    weak.Pointer[App]
	logger contract.Logger
	hook   func(context.Context, any)
}

// packageEntry returns a's entry, pushing a new one (a becomes the newest
// app) when a has none. packageStateMu must be held.
func packageEntry(a *App) *packageInstall {
	key := weak.Make(a)
	for _, e := range packageStack {
		if e.app == key {
			return e
		}
	}
	e := &packageInstall{app: key}
	packageStack = append(packageStack, e)
	return e
}

// packageOwner returns the entry whose values are installed: the newest
// app's, or nil with no app. packageStateMu must be held.
func packageOwner() *packageInstall {
	if len(packageStack) == 0 {
		return nil
	}
	return packageStack[len(packageStack)-1]
}

// installPackageLoggers records l as a's package logger (nil means the
// standalone fallback logger) and hands it to the async and trace packages
// when a owns the state.
func installPackageLoggers(a *App, l contract.Logger) {
	packageStateMu.Lock()
	defer packageStateMu.Unlock()
	e := packageEntry(a)
	e.logger = l
	if packageOwner() == e {
		applyPackageLoggers(l)
	}
}

// installPanicHook records hook as a's async panic hook (nil means no
// hook) and installs it when a owns the state.
func installPanicHook(a *App, hook func(context.Context, any)) {
	packageStateMu.Lock()
	defer packageStateMu.Unlock()
	e := packageEntry(a)
	e.hook = hook
	if packageOwner() == e {
		async.SetPanicHook(hook)
	}
}

// releasePackageState removes a's entry. When a owned the state, the
// previous live app's values are installed, or the packages' defaults when
// no app is left. When another app owns it, nothing installed changes.
// App.Shutdown calls it before closing the logger, and the cleanup of a
// failed New calls it; an app with no entry is a no-op. The async and
// trace SetLogger calls it makes return once every line in flight through
// the logger they replace has been written, so the caller may close that
// logger afterwards.
func releasePackageState(a *App) {
	if a == nil {
		return
	}
	key := weak.Make(a)
	packageStateMu.Lock()
	defer packageStateMu.Unlock()
	idx := -1
	for i, e := range packageStack {
		if e.app == key {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	wasOwner := idx == len(packageStack)-1
	packageStack = append(packageStack[:idx], packageStack[idx+1:]...)
	if !wasOwner {
		return
	}
	next := packageOwner()
	var (
		l    contract.Logger
		hook func(context.Context, any)
	)
	if next != nil {
		l, hook = next.logger, next.hook
	}
	async.SetPanicHook(hook)
	applyPackageLoggers(l)
}

// applyPackageLoggers hands l to the async and trace packages.
func applyPackageLoggers(l contract.Logger) {
	async.SetLogger(l)
	trace.SetLogger(l)
}
