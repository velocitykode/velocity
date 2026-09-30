// Package drivers stands in for the module's orm/drivers: the checker
// knows StatementObserver by package path and name.
package drivers

type StatementObserver interface {
	Observing() bool
	ObserveStatement(sql string)
}

// Other is an interface of the same package that is not the observer.
type Other interface {
	Name() string
}
