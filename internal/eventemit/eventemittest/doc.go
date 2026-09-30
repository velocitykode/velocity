// Package eventemittest holds test support for components that hand their
// events over through an eventemit.Emitter: Receiving builds an emitter
// whose dispatcher hands every event to a test function, so a test calls a
// component's event helper exactly as the component does.
package eventemittest
