package contract

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// WantsJSON reports whether r asks for a JSON response. The preferred
// Accept media range decides (see PreferredMediaRange: highest q, the
// first listed among equals, q=0 excluded): application/json or any type
// whose subtype ends in +json (application/problem+json,
// application/vnd.api+json) wants JSON. An XMLHttpRequest
// (X-Requested-With, any case) wants JSON when no range is preferred or
// the preferred one is */*. An Inertia request (see IsInertia) never
// wants JSON: it expects an Inertia page or a location reload.
func WantsJSON(r *http.Request) bool {
	if r == nil || IsInertia(r) {
		return false
	}
	media := PreferredMediaRange(r.Header.Get("Accept"))
	if media == "application/json" || strings.HasSuffix(media, "+json") {
		return true
	}
	if media == "" || media == "*/*" {
		return strings.EqualFold(r.Header.Get("X-Requested-With"), "XMLHttpRequest")
	}
	return false
}

// IsInertia reports whether r is an Inertia request: its X-Inertia header
// is "true" in any case. Any other value, or no header, is not.
func IsInertia(r *http.Request) bool {
	return r != nil && strings.EqualFold(r.Header.Get("X-Inertia"), "true")
}

// MediaRange is one media range of an Accept header value.
type MediaRange struct {
	// Type is the media type ("type/subtype" or a wildcard), lower-cased,
	// without parameters.
	Type string
	// Q is the quality value: 1 when the range carries none or an
	// unparsable one, at most 1.
	Q float64
}

// ParseAccept parses an Accept header value into its media ranges,
// highest quality first; ranges of equal quality keep their listed order.
// Empty ranges and ranges with q=0 (not acceptable) are dropped.
func ParseAccept(accept string) []MediaRange {
	ranges := make([]MediaRange, 0, strings.Count(accept, ",")+1)
	for rest := accept; rest != ""; {
		var mr MediaRange
		var ok bool
		mr, rest, ok = nextMediaRange(rest)
		if ok {
			ranges = append(ranges, mr)
		}
	}
	sort.SliceStable(ranges, func(i, j int) bool { return ranges[i].Q > ranges[j].Q })
	return ranges
}

// PreferredMediaRange returns the type of the first range ParseAccept
// would return for accept (highest q, the first listed among equals, q=0
// excluded), or "" when accept names none. It does not allocate for a
// lower-case header.
func PreferredMediaRange(accept string) string {
	var best MediaRange
	found := false
	for rest := accept; rest != ""; {
		var mr MediaRange
		var ok bool
		mr, rest, ok = nextMediaRange(rest)
		if ok && (!found || mr.Q > best.Q) {
			best, found = mr, true
		}
	}
	return best.Type
}

// nextMediaRange parses the media range at the start of s (up to the
// first comma) and returns it with the text after that comma. ok is false
// for an empty range or one with q=0.
func nextMediaRange(s string) (mr MediaRange, rest string, ok bool) {
	raw := s
	if i := strings.IndexByte(s, ','); i >= 0 {
		raw, rest = s[:i], s[i+1:]
	}
	params := ""
	if i := strings.IndexByte(raw, ';'); i >= 0 {
		raw, params = raw[:i], raw[i+1:]
	}
	media := strings.TrimSpace(raw)
	if media == "" {
		return MediaRange{}, rest, false
	}
	q := qValue(params)
	if q <= 0 {
		return MediaRange{}, rest, false
	}
	return MediaRange{Type: strings.ToLower(media), Q: q}, rest, true
}

// qValue returns the first q parameter among ";"-separated params (the
// name in any case), capped at 1, or 1 when it is absent or unparsable.
func qValue(params string) float64 {
	for params != "" {
		param := params
		params = ""
		if i := strings.IndexByte(param, ';'); i >= 0 {
			param, params = param[:i], param[i+1:]
		}
		param = strings.TrimSpace(param)
		if len(param) < 2 || (param[0] != 'q' && param[0] != 'Q') || param[1] != '=' {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(param[2:]), 64)
		if err != nil || v != v || v > 1 {
			return 1
		}
		return v
	}
	return 1
}

// AppendVary adds v to h's Vary header unless an existing Vary value
// already lists it (compared without case), so every writer negotiating on
// a request header declares it without duplicating another's entry.
func AppendVary(h http.Header, v string) {
	for _, existing := range h.Values("Vary") {
		for existing != "" {
			part := existing
			existing = ""
			if i := strings.IndexByte(part, ','); i >= 0 {
				part, existing = part[:i], part[i+1:]
			}
			if strings.EqualFold(strings.TrimSpace(part), v) {
				return
			}
		}
	}
	h.Add("Vary", v)
}
