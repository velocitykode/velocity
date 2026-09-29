package lockheld

// A test passing a func value does not open withTested's parameter.
func useTested(k *K) { k.withTested(k.hook) }
