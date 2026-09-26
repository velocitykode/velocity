package stores

// ConsumptionScope is how far a csrf.AtomicConsumer's single-use guarantee
// reaches.
type ConsumptionScope int

const (
	// ConsumedEverywhere: every instance that can accept a token consults
	// the same consumption record, so a token is accepted once across the
	// whole deployment. A per-process store whose tokens never leave the
	// process (MemoryStore) is one; a store over a cache every
	// instance shares is another.
	ConsumedEverywhere ConsumptionScope = iota + 1

	// ConsumedPerInstance: the token travels to other instances (it lives
	// in the session cookie or a shared session record) while the
	// consumption record stays in this process. A token is accepted once
	// per instance: a replay on the instance that consumed it is refused,
	// and a replay on another instance can be accepted once there.
	// SessionBagStore is one.
	ConsumedPerInstance
)
