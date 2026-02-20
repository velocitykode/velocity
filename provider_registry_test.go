package velocity

import "testing"

func TestProviderRegistry_AddSingle(t *testing.T) {
	var calls []string
	p := &trackingProvider{name: "A", calls: &calls}

	var reg ProviderRegistry
	reg.Add(p)

	if len(reg.providers) != 1 {
		t.Fatalf("got %d providers, want 1", len(reg.providers))
	}
}

func TestProviderRegistry_AddMultiple(t *testing.T) {
	var calls []string
	pA := &trackingProvider{name: "A", calls: &calls}
	pB := &trackingProvider{name: "B", calls: &calls}
	pC := &trackingProvider{name: "C", calls: &calls}

	var reg ProviderRegistry
	reg.Add(pA, pB, pC)

	if len(reg.providers) != 3 {
		t.Fatalf("got %d providers, want 3", len(reg.providers))
	}
}

func TestProviderRegistry_AddAccumulates(t *testing.T) {
	var calls []string
	pA := &trackingProvider{name: "A", calls: &calls}
	pB := &trackingProvider{name: "B", calls: &calls}

	var reg ProviderRegistry
	reg.Add(pA)
	reg.Add(pB)

	if len(reg.providers) != 2 {
		t.Fatalf("got %d providers, want 2", len(reg.providers))
	}
}
