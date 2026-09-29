package velocity

import (
	"context"
	"sync"

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
// Every app that wires the state keeps one installation on a stack, oldest
// first, and holds it as its registration token (App.packageInstall). An
// app's installation (hook and both loggers) is published in one step
// (installPackageState): a release never sees an app that owns the hook
// but not yet the loggers. An app's later wiring (a lifecycle boundary
// after a module swapped Services.Log or Services.Errors) updates its own
// installation, and reaches the packages only while the app owns the
// state: an older app never takes it back from a newer one. An app's
// Shutdown, or the cleanup of its failed New, removes its installation
// (releasePackageState); when that app owned the state, the previous live
// app's values are installed again, and with no app left the packages go
// back to their defaults (no hook, the standalone fallback logger). An app
// never clears or replaces another app's installation, and a removed
// installation is cleared, so the stack keeps no reference to a shut-down
// app.
//
// Nothing here waits on the hook or the loggers: packageStateMu guards
// only the stack and the three package stores, and no code under it calls
// a hook, a logger or other app code. A line or panic report already in
// flight may therefore reach an app's logger after its release; the
// built-in file logger sends such a late warning or error to the
// standalone fallback logger once closed (see log/file).
//
// Known limits: the hook and loggers are per process, not per goroutine.
// A panic recovered in a goroutine an older app started, while a newer app
// lives, is reported to the newer app's error handler. An app that is
// never shut down stays live and stays reachable: when a newer app shuts
// down, the state returns to it.
var (
	packageStateMu sync.Mutex
	packageStack   []*packageInstall
)

// packageInstall is one app's values for the process-wide package state:
// its async panic hook and its package logger. A nil field is installed
// as nil (the package default). Guarded by packageStateMu.
type packageInstall struct {
	logger contract.Logger
	hook   func(context.Context, any)
}

// installPackageState records the panic hook reporting to the error
// handler a.Services.Errors holds now, and the logger a.Services.Log holds
// now (nil means the standalone fallback logger), as a's installation,
// pushing a new one (a becomes the newest app) when a has none, and hands
// both to the async and trace packages in the same step when a owns the
// state. The hook is built before the lock is taken; under it only the
// stack and the package stores change.
func installPackageState(a *App) {
	hook := buildPanicHook(a.Services.Errors)
	l := a.Services.Log
	packageStateMu.Lock()
	defer packageStateMu.Unlock()
	e := a.packageInstall
	if e == nil {
		e = &packageInstall{}
		a.packageInstall = e
		packageStack = append(packageStack, e)
	}
	e.hook, e.logger = hook, l
	if packageStack[len(packageStack)-1] == e {
		applyPackageState(e)
	}
}

// releasePackageState removes a's installation. When a owned the state,
// the previous live app's values are installed, or the packages' defaults
// when no app is left. When another app owns it, nothing installed
// changes. App.Shutdown calls it before closing the logger, and the
// cleanup of a failed New calls it; a nil app, an app with no installation
// and a second release are no-ops.
func releasePackageState(a *App) {
	if a == nil {
		return
	}
	packageStateMu.Lock()
	defer packageStateMu.Unlock()
	e := a.packageInstall
	if e == nil {
		return
	}
	a.packageInstall = nil
	e.hook, e.logger = nil, nil
	idx := -1
	for i, s := range packageStack {
		if s == e {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	last := len(packageStack) - 1
	copy(packageStack[idx:], packageStack[idx+1:])
	packageStack[last] = nil
	packageStack = packageStack[:last]
	if last == 0 {
		packageStack = nil
	}
	if idx != last {
		return
	}
	var next *packageInstall
	if last > 0 {
		next = packageStack[last-1]
	}
	applyPackageState(next)
}

// applyPackageState hands e's hook and logger to the async and trace
// packages, or their defaults when e is nil. packageStateMu must be held.
// Each store only swaps a pointer: nothing here calls the hook or the
// logger.
func applyPackageState(e *packageInstall) {
	var (
		l    contract.Logger
		hook func(context.Context, any)
	)
	if e != nil {
		l, hook = e.logger, e.hook
	}
	async.SetPanicHook(hook)
	async.SetLogger(l)
	trace.SetLogger(l)
}
