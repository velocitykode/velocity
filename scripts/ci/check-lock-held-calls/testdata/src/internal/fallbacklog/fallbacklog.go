// Package fallbacklog stands in for the module's fallback logger, which
// the checker does not count as user code.
package fallbacklog

import "example.com/lockheld/contract"

type Logger struct{}

func (Logger) Debug(string, ...any)          {}
func (Logger) Info(string, ...any)           {}
func (Logger) Warn(string, ...any)           {}
func (Logger) Error(string, ...any)          {}
func (Logger) Fatal(string, ...any)          {}
func (l Logger) With(...any) contract.Logger { return l }
