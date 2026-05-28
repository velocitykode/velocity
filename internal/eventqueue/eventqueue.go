// Package eventqueue is the internal-only home of the bootstrap-only
// queue-integration wiring for the events package. Sweep 1b moved
// InitializeQueueIntegration here so that consumer apps cannot call the
// bootstrap-only hook directly from their own code (it is wired exactly
// once from velocity.App.bootstrap) and so a future refactor of the wiring
// is not constrained by an exported-API freeze.
//
// The events.InitializeQueueIntegration symbol is kept as a deprecated
// shim that delegates here so existing white-box tests in events/ keep
// compiling. New callers MUST import this package instead.
package eventqueue

import (
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/queue"
)

// InitializeQueueIntegration wires the queue-integration plumbing that turns
// queued listeners from a silent-drop hole (H-22) into a production-ready
// path. It is idempotent: repeated calls overwrite the previous wiring with
// the new arguments, so consumers can re-invoke it during hot config reloads.
//
// Arguments:
//   - dispatcher: the QueueIntegratedDispatcher to bind to the queue driver.
//     May be nil when consumers manage the dispatcher separately.
//   - driver: the queue.Driver that pushed jobs land on. May be nil when
//     consumers only want to register the job factory and reporter.
//   - reporter: optional callback that fires from EventListenerJob.Failed.
//     Nil disables the reporter (calls become no-ops); pass a closure over
//     exceptions.Handler.Report to route to the framework's exception sink.
//
// This is the canonical entry point. The events package retains a
// deprecated shim with the same signature for backwards compatibility
// during the v0.x line; all framework code MUST import eventqueue
// instead.
func InitializeQueueIntegration(dispatcher *events.QueueIntegratedDispatcher, driver queue.Driver, reporter events.FailureReporter) {
	events.InitializeQueueIntegration(dispatcher, driver, reporter)
}
