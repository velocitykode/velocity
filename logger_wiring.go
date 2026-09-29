package velocity

import (
	"github.com/velocitykode/velocity/contract"
)

// wireInstanceLoggers hands the logger a.Services.Log holds now to every
// service in loggerWiringCandidates that takes one (contract.LoggerAware).
// The async and trace package loggers are process-wide and recorded
// separately, with the async panic hook, by installPackageState.
// Services that own other logger-aware values pass it on: the ORM manager
// to its connections' query logger, the view engine to its bond, the auth
// manager to its schemes and hasher, the CSRF instance to its token store,
// the cache manager to the stores it builds, the notification manager to
// its channels, the mailer to its driver.
//
// It runs from wireInstanceEvents, so at every lifecycle boundary the event
// dispatcher is handed out at (in New before and after the WithModules
// lifecycle, and in bootstrap after the chain modules' Start, after the
// event registration step and after the Errors step), whether or not
// events are enabled. A module or callback that replaces Services.Log, or a
// logger-aware service, therefore has the replacement wired at the next
// boundary. The value is read once per sweep, like the dispatcher, so no
// service reads Services.Log from its own goroutines; every SetLogger it
// calls is safe while the service runs. A nil Services.Log hands nil, which
// puts each value back on its no-logger default (the framework's standalone
// fallback logger).
//
// The router is not swept: New gives it appLogger, which forwards each line
// to the current Services.Log. Registry components are app-built values
// and keep the logger the app gave them.
func wireInstanceLoggers(a *App) {
	l := a.Services.Log
	for _, c := range loggerWiringCandidates(a) {
		if la, ok := c.value.(contract.LoggerAware); ok {
			la.SetLogger(l)
		}
	}
}

// loggerCandidate is one Services field the logger sweep offers the app
// logger to, by field name.
type loggerCandidate struct {
	name  string
	value any
}

// loggerWiringCandidates returns every Services field whose value can take
// the app logger: every interface field except Log (the logger itself) and
// RedirectAllowlist (the router, see wireInstanceLoggers). A nil field or a
// value without a logger seam is skipped by the sweep. The conformance test
// in logger_wiring_test.go sweeps app.Services by reflection and fails when
// a field is missing here.
func loggerWiringCandidates(a *App) []loggerCandidate {
	s := a.Services
	return []loggerCandidate{
		{"Errors", s.Errors},
		{"Crypto", s.Crypto},
		{"DB", s.DB},
		{"Auth", s.Auth},
		{"CSRF", s.CSRF},
		{"View", s.View},
		{"Cache", s.Cache},
		{"Events", s.Events},
		{"Queue", s.Queue},
		{"Storage", s.Storage},
		{"Scheduler", s.Scheduler},
		{"Mail", s.Mail},
		{"Notification", s.Notification},
		{"Validator", s.Validator},
	}
}
