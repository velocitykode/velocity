// Package pipeline provides a generic, fluent abstraction for sending a value
// through a series of processing stages. Each stage receives the current value
// and a next function, and decides whether to pass control forward,
// short-circuit, or return an error.
//
// Stages can be defined as structs implementing the [Stage] interface, or as
// inline functions using the [Pipe] adapter — both work interchangeably with
// [Pipeline.Through].
//
// Struct-based stages (Laravel-style):
//
//	type ValidateOrder struct{}
//
//	func (v *ValidateOrder) Handle(o *Order, next func(*Order) error) error {
//	    if o.Total <= 0 {
//	        return errors.New("invalid total")
//	    }
//	    return next(o)
//	}
//
//	type ApplyDiscount struct{ Rate float64 }
//
//	func (a *ApplyDiscount) Handle(o *Order, next func(*Order) error) error {
//	    o.Total *= (1 - a.Rate)
//	    return next(o)
//	}
//
//	err := pipeline.New[*Order]().
//	    Send(order).
//	    Through(
//	        &ValidateOrder{},
//	        &ApplyDiscount{Rate: 0.1},
//	        &ChargePayment{},
//	    ).
//	    ThenReturn()
//
// Function-based stages:
//
//	log := pipeline.Pipe[*Order](func(o *Order, next func(*Order) error) error {
//	    fmt.Println("processing order", o.ID)
//	    return next(o)
//	})
//
//	err := pipeline.New[*Order]().
//	    Send(order).
//	    Through(log, &ValidateOrder{}).
//	    Then(func(o *Order) error {
//	        return save(o)
//	    })
package pipeline
