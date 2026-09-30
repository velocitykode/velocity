// Package testnet gives tests a listener to hand to the code under test.
//
// A test that needs a server on a free port opens the listener here and
// passes the listener itself to the server (grpc.WithListener and the
// like), never its port number: a port read from a listener and released
// for the server to bind again can be taken by any other process in
// between. scripts/ci/check-released-ports rejects that pattern.
package testnet
