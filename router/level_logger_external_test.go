package router_test

import "github.com/velocitykode/velocity/contract"

// levelLogger is a contract.Logger that hands error- and warn-level lines
// to the funcs set and drops every other line, so a test observes the
// router's default error path per level.
type levelLogger struct {
	onError func(msg string, kvs ...any)
	onWarn  func(msg string, kvs ...any)
}

func (l levelLogger) Error(msg string, kvs ...any) {
	if l.onError != nil {
		l.onError(msg, kvs...)
	}
}

func (l levelLogger) Warn(msg string, kvs ...any) {
	if l.onWarn != nil {
		l.onWarn(msg, kvs...)
	}
}

func (levelLogger) Debug(string, ...any) {}
func (levelLogger) Info(string, ...any)  {}
func (levelLogger) Fatal(string, ...any) {}

func (l levelLogger) With(kvs ...any) contract.Logger { return contract.BindFields(l, kvs...) }
