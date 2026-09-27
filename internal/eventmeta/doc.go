// Package eventmeta builds contract.EventMeta, the envelope every framework
// event embeds, from the context the event is dispatched under. Current is
// for an event that records work running under the context's span (a job,
// a scheduled run, a command); Child is for one that records an operation
// running as a span of its own under it (a cache call). ErrorText and
// TextError are the two directions of an error field's JSON form: its text.
//
// The router and the packages in its dependency graph (the scheduler)
// build their envelopes from contract and trace themselves, so the router's
// dependency graph does not grow.
package eventmeta
