package postmark

import (
	"strings"
	"sync"
)

// allowedStreams enumerates the Message Streams recognised by default.
// Callers that use custom broadcast streams can replace the set via
// ConfigureAllowedStreams.
var (
	allowedStreams   = map[string]struct{}{"outbound": {}, "broadcast": {}, "transactional": {}, "inbound": {}}
	allowedStreamsMu sync.RWMutex
)

// ConfigureAllowedStreams replaces the set of allowed Postmark message
// streams. Names are lower-cased. Passing an empty slice restores the defaults.
func ConfigureAllowedStreams(streams []string) {
	allowedStreamsMu.Lock()
	defer allowedStreamsMu.Unlock()
	if len(streams) == 0 {
		allowedStreams = map[string]struct{}{"outbound": {}, "broadcast": {}, "transactional": {}, "inbound": {}}
		return
	}
	next := make(map[string]struct{}, len(streams))
	for _, s := range streams {
		next[strings.ToLower(strings.TrimSpace(s))] = struct{}{}
	}
	allowedStreams = next
}

// IsAllowedStream reports whether a stream name passes the allowlist.
func IsAllowedStream(name string) bool {
	allowedStreamsMu.RLock()
	defer allowedStreamsMu.RUnlock()
	_, ok := allowedStreams[strings.ToLower(strings.TrimSpace(name))]
	return ok
}
