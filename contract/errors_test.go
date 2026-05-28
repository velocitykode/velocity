package contract_test

import (
	"errors"
	"testing"

	"github.com/velocitykode/velocity/broadcast"
	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
	cryptodrivers "github.com/velocitykode/velocity/crypto/drivers"
	"github.com/velocitykode/velocity/queue"
	"github.com/velocitykode/velocity/storage"
)

// TestSentinelStability pins the .Error() strings and the identity of every
// hoisted cross-package sentinel. The contract-package error is the single
// source of truth; package-local aliases (queue.ErrJobNotFound,
// cache.ErrStoreNotFound, etc.) MUST resolve to the same underlying value
// so errors.Is matches against either name.
//
// Changing any string below is a breaking change for callers that string-
// match (gauntlet: log parsers, integration test diff assertions). Pre-1.0
// it is allowed; post-1.0 it requires a major-version bump.
func TestSentinelStability(t *testing.T) {
	t.Parallel()

	stable := []struct {
		name string
		err  error
		want string
	}{
		{"ErrJobNotFound", contract.ErrJobNotFound, "velocity/queue: job not found"},
		{"ErrBatchNotFound", contract.ErrBatchNotFound, "velocity/queue: batch not found"},
		{"ErrCacheStoreNotFound", contract.ErrCacheStoreNotFound, "velocity/cache: store not found"},
		{"ErrCacheKeyNotFound", contract.ErrCacheKeyNotFound, "velocity/cache: key not found"},
		{"ErrFileNotFound", contract.ErrFileNotFound, "velocity/storage: file not found"},
		{"ErrDiskNotFound", contract.ErrDiskNotFound, "velocity/storage: disk not found"},
		{"ErrBroadcastDriverNotFound", contract.ErrBroadcastDriverNotFound, "velocity/broadcast: driver not found"},
		{"ErrInvalidKey", contract.ErrInvalidKey, "velocity/crypto: invalid encryption key"},
		{"ErrInvalidPreviousKey", contract.ErrInvalidPreviousKey, "velocity/crypto: invalid previous key"},
		{"ErrInvalidPayload", contract.ErrInvalidPayload, "velocity/crypto: invalid payload format"},
	}
	for _, tc := range stable {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("%s: Error() = %q, want %q (sentinel string changed; pre-1.0 review required)", tc.name, got, tc.want)
		}
	}

	// Aliases must preserve identity so errors.Is keeps matching against
	// the package-local names that already shipped.
	aliases := []struct {
		name  string
		alias error
		canon error
	}{
		{"queue.ErrJobNotFound", queue.ErrJobNotFound, contract.ErrJobNotFound},
		{"queue.ErrBatchNotFound", queue.ErrBatchNotFound, contract.ErrBatchNotFound},
		{"cache.ErrStoreNotFound", cache.ErrStoreNotFound, contract.ErrCacheStoreNotFound},
		{"cache.ErrKeyNotFound", cache.ErrKeyNotFound, contract.ErrCacheKeyNotFound},
		{"storage.ErrFileNotFound", storage.ErrFileNotFound, contract.ErrFileNotFound},
		{"storage.ErrDiskNotFound", storage.ErrDiskNotFound, contract.ErrDiskNotFound},
		{"broadcast.ErrDriverNotFound", broadcast.ErrDriverNotFound, contract.ErrBroadcastDriverNotFound},
		{"crypto.ErrInvalidKey", crypto.ErrInvalidKey, contract.ErrInvalidKey},
		{"crypto.ErrInvalidPreviousKey", crypto.ErrInvalidPreviousKey, contract.ErrInvalidPreviousKey},
		{"crypto.ErrInvalidPayload", crypto.ErrInvalidPayload, contract.ErrInvalidPayload},
		{"crypto/drivers.ErrInvalidPayload", cryptodrivers.ErrInvalidPayload, contract.ErrInvalidPayload},
	}
	for _, tc := range aliases {
		if tc.alias != tc.canon {
			t.Errorf("%s: identity != contract sentinel; errors.Is would break", tc.name)
		}
		if !errors.Is(tc.alias, tc.canon) {
			t.Errorf("%s: errors.Is(alias, canon) returned false", tc.name)
		}
	}
}
