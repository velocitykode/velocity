package drivers

import (
	"github.com/velocitykode/velocity/contract"
)

type discardLogger struct{}

func (discardLogger) Debug(string, ...any)          {}
func (discardLogger) Info(string, ...any)           {}
func (discardLogger) Warn(string, ...any)           {}
func (discardLogger) Error(string, ...any)          {}
func (discardLogger) Fatal(string, ...any)          {}
func (l discardLogger) With(...any) contract.Logger { return l }
