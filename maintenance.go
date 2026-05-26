package velocity

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/hkdf"

	"github.com/velocitykode/velocity/internal/maintpath"
	"github.com/velocitykode/velocity/router"
)

// Maintenance constants.
const (
	// maintenanceBypassCookie is the cookie name used to grant a browser
	// bypass while the application is in maintenance mode.
	maintenanceBypassCookie = "velocity_maintenance_bypass"
	// maintenanceBypassDefaultTTL is the default lifetime of a freshly
	// minted bypass cookie. Mirrors Laravel's 12h window.
	maintenanceBypassDefaultTTL = 12 * time.Hour
	// maintenanceMarkerMaxSize bounds reads of the down-file.
	maintenanceMarkerMaxSize = 64 << 10
	// maintenanceBypassInfo separates the HKDF info string for the bypass
	// MAC subkey from other crypto subsystems.
	maintenanceBypassInfo = "velocity-maintenance-bypass-v1"
	// maintenanceMACContext is the context label prefixed to the timestamp
	// before MAC computation so the MAC commits to a specific purpose.
	maintenanceMACContext = "maintenance-bypass:"
)

// maintenancePathLogOnce guards the one-time WARN log emitted on first
// resolution of the marker path. Operators see exactly one line per process
// announcing which directory is being watched, removing the silent-cwd-drift
// failure mode the M-39 finding flagged.
var maintenancePathLogOnce sync.Once

// maintenanceMarkerPath returns the absolute path of the down-file. The
// resolution policy lives in internal/maintpath so the console writer and
// the runtime reader agree on a single source of truth. On the first call
// the resolved path is logged at WARN so operators can verify which
// directory the framework is watching; subsequent calls are silent.
//
// Returns ("", err) when VELOCITY_MAINTENANCE_ROOT is set but fails
// validation. Callers treat that as "no marker file present" so an
// operator typo cannot accidentally pin the app into maintenance.
func maintenanceMarkerPath() (string, error) {
	p, err := maintpath.MarkerPath()
	maintenancePathLogOnce.Do(func() {
		if err != nil {
			slog.Default().Warn(
				"maintenance marker path resolution failed",
				"error", err.Error(),
				"source", maintpath.Source(),
			)
			return
		}
		slog.Default().Warn(
			"maintenance marker path resolved",
			"path", p,
			"source", maintpath.Source(),
		)
	})
	return p, err
}

// downPayload mirrors the JSON shape written by console.Down. Only fields
// the middleware actually reads are declared.
type downPayload struct {
	Secret     string `json:"secret,omitempty"`
	RetryAfter int    `json:"retry_after,omitempty"`
	Time       string `json:"time,omitempty"`
}

// readDownPayload reads the down-file and returns the parsed payload.
// Returns (nil, false) when the file is absent. Returns (payload, true)
// when present and parseable. Malformed files are treated as "present
// with empty payload" so maintenance still applies but bypass is disabled.
func readDownPayload(path string) (*downPayload, bool) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false
		}
		// Unreadable file. Still in maintenance, bypass unavailable.
		return &downPayload{}, true
	}
	defer f.Close()

	limited := io.LimitReader(f, maintenanceMarkerMaxSize)
	dec := json.NewDecoder(limited)
	dec.DisallowUnknownFields()

	var payload downPayload
	if err := dec.Decode(&payload); err != nil {
		// Treat malformed JSON as maintenance-with-no-bypass rather than
		// failing open. Operators can correct the file via `vel up`.
		return &downPayload{}, true
	}
	return &payload, true
}

// defaultMaintenanceExcludePaths is the set of request path prefixes that
// bypass the 503 short-circuit by default. These are the paths every load
// balancer probe, container orchestrator, and runtime health checker hits.
// 503'ing them removes the instance from rotation seconds after the
// operator runs `vel down`, which is the opposite of what they intended.
//
// Webhook endpoints are intentionally NOT included by default because their
// path layout varies per application (/webhooks/stripe, /api/webhooks/...
// /hooks/github). Operators add their own via WithMaintenanceExcludePaths
// or the VELOCITY_MAINTENANCE_EXCLUDE_PATHS env var.
var defaultMaintenanceExcludePaths = []string{"/healthz", "/livez", "/readyz"}

// MaintenanceOption configures PreventRequestsDuringMaintenance.
type MaintenanceOption func(*maintenanceConfig)

// maintenanceConfig is the resolved option set for one middleware instance.
type maintenanceConfig struct {
	// excludePaths holds the request-path prefixes that bypass the 503
	// short-circuit. Stored as a deduped slice, scanned linearly per
	// request; on the order of a handful of entries so prefix-trie not
	// warranted.
	excludePaths []string
}

// WithMaintenanceExcludePaths appends path prefixes to the bypass list.
// Each call adds to whatever the defaults + env already produced; pass
// the empty string nowhere because that would match every request and
// silently disable the middleware.
func WithMaintenanceExcludePaths(paths ...string) MaintenanceOption {
	return func(c *maintenanceConfig) {
		for _, p := range paths {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			c.excludePaths = appendUniquePath(c.excludePaths, p)
		}
	}
}

// resolveMaintenanceConfig builds the per-instance config. Order:
// defaults -> env -> caller options. Caller options always win.
func resolveMaintenanceConfig(opts ...MaintenanceOption) *maintenanceConfig {
	cfg := &maintenanceConfig{
		excludePaths: append([]string{}, defaultMaintenanceExcludePaths...),
	}
	if raw, ok := os.LookupEnv("VELOCITY_MAINTENANCE_EXCLUDE_PATHS"); ok {
		for _, p := range strings.Split(raw, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			cfg.excludePaths = appendUniquePath(cfg.excludePaths, p)
		}
	}
	for _, o := range opts {
		o(cfg)
	}
	return cfg
}

// appendUniquePath appends p to ps if not already present (string equal).
// Caller path matching is by prefix below; uniqueness here is for the
// stored list, not for the matching semantics.
func appendUniquePath(ps []string, p string) []string {
	for _, existing := range ps {
		if existing == p {
			return ps
		}
	}
	return append(ps, p)
}

// matchesExcludePath returns true when reqPath is in or under any of the
// configured exclude entries. Match is prefix-only and deliberately strict:
// "/healthz" excludes "/healthz" and "/healthz/anything"; "/healthz" does
// NOT exclude "/healthzoo" because the next char after the prefix must be
// the end of the path or a "/". Mirrors how Laravel's Except() matcher
// behaves for non-wildcard entries.
func matchesExcludePath(reqPath string, excludes []string) bool {
	for _, e := range excludes {
		if reqPath == e {
			return true
		}
		if strings.HasPrefix(reqPath, e) && len(reqPath) > len(e) && reqPath[len(e)] == '/' {
			return true
		}
	}
	return false
}

// PreventRequestsDuringMaintenance returns middleware that returns a 503
// Service Unavailable response while the application is in maintenance mode.
//
// If the down-file contains a secret and the request path matches "/" + secret,
// the middleware mints a signed bypass cookie and redirects to "/". Subsequent
// requests carrying a valid, non-expired bypass cookie are served normally.
// The bypass MAC is keyed off the operator-supplied secret via HKDF so leaking
// APP_KEY alone cannot grant a bypass.
//
// Paths in the configured exclude list pass through to the next handler
// even while in maintenance. The defaults ("/healthz", "/livez", "/readyz")
// keep load-balancer probes happy; operators add webhook prefixes through
// WithMaintenanceExcludePaths or VELOCITY_MAINTENANCE_EXCLUDE_PATHS env.
func PreventRequestsDuringMaintenance(opts ...MaintenanceOption) router.MiddlewareFunc {
	cfg := resolveMaintenanceConfig(opts...)
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			path, err := maintenanceMarkerPath()
			if err != nil {
				// Misconfigured root: treat as "not in maintenance" so a
				// bad env var cannot lock everyone out. The error is
				// already logged once via maintenancePathLogOnce.
				return next(c)
			}
			payload, down := readDownPayload(path)
			if !down {
				return next(c)
			}

			// Excluded paths (health probes, webhooks) must remain reachable
			// while the application is otherwise in maintenance, otherwise
			// load balancers will tear out the instance and webhook senders
			// will back off / retry-storm.
			if matchesExcludePath(c.Request.URL.Path, cfg.excludePaths) {
				return next(c)
			}

			// Bypass mint: secret-equals-path mints a cookie + redirect.
			if payload.Secret != "" && c.Request.URL.Path == "/"+payload.Secret {
				cookie := mintMaintenanceBypassCookie(payload.Secret, maintenanceBypassDefaultTTL)
				c.SetCookie(cookie)
				// Redirect via http.Redirect directly because c.Redirect
				// rewrites cross-host targets; "/" is always safe but
				// going through the same fixed path makes the intent clear.
				http.Redirect(c.Response, c.Request, "/", http.StatusFound)
				return nil
			}

			// Bypass check: valid cookie skips maintenance.
			if payload.Secret != "" && hasValidBypassCookie(c.Request, payload.Secret) {
				return next(c)
			}

			return c.JSON(http.StatusServiceUnavailable, map[string]string{
				"message": "Service Unavailable",
			})
		}
	}
}

// isDownForMaintenance checks whether the .vel/down marker file exists.
// Retained for backwards compatibility with callers that only need a
// boolean status check.
func isDownForMaintenance() bool {
	path, err := maintenanceMarkerPath()
	if err != nil {
		return false
	}
	_, statErr := os.Stat(path)
	return statErr == nil
}

// deriveMaintenanceMACKey derives a 32-byte HMAC key from the operator-
// supplied secret. APP_KEY is mixed in as HKDF salt when available so a
// stolen secret on its own is not sufficient to forge a bypass on a
// different application, but a leaked APP_KEY cannot grant a bypass
// without the secret either.
func deriveMaintenanceMACKey(secret string) ([]byte, error) {
	if secret == "" {
		return nil, errors.New("velocity/maintenance: empty secret")
	}
	salt := []byte(os.Getenv("APP_KEY"))
	if len(salt) == 0 {
		salt = []byte("velocity-maintenance-bypass-default-salt")
	}
	r := hkdf.New(sha256.New, []byte(secret), salt, []byte(maintenanceBypassInfo))
	out := make([]byte, 32)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, err
	}
	return out, nil
}

// computeMaintenanceMAC returns HMAC-SHA256(macKey, context || expires) as
// raw bytes. Callers are responsible for encoding.
func computeMaintenanceMAC(macKey []byte, expiresUnix int64) []byte {
	mac := hmac.New(sha256.New, macKey)
	mac.Write([]byte(maintenanceMACContext))
	mac.Write([]byte(strconv.FormatInt(expiresUnix, 10)))
	return mac.Sum(nil)
}

// mintMaintenanceBypassCookie returns a signed bypass cookie. The cookie
// value is base64(expires_unix : hex(mac)). HttpOnly is always set; Secure
// is set unless APP_ENV is "development" or "testing".
func mintMaintenanceBypassCookie(secret string, ttl time.Duration) *http.Cookie {
	expires := time.Now().Add(ttl).Unix()
	macKey, err := deriveMaintenanceMACKey(secret)
	if err != nil {
		// Fall back to a deliberately invalid cookie. The middleware will
		// reject it on the next request, which is preferable to panicking
		// in library code.
		return &http.Cookie{
			Name:   maintenanceBypassCookie,
			Value:  "",
			MaxAge: -1,
			Path:   "/",
		}
	}
	mac := computeMaintenanceMAC(macKey, expires)
	raw := strconv.FormatInt(expires, 10) + ":" + hex.EncodeToString(mac)
	value := base64.RawURLEncoding.EncodeToString([]byte(raw))

	return &http.Cookie{
		Name:     maintenanceBypassCookie,
		Value:    value,
		Path:     "/",
		Expires:  time.Unix(expires, 0),
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   shouldUseSecureBypassCookie(),
		SameSite: http.SameSiteLaxMode,
	}
}

// hasValidBypassCookie returns true when the request carries a non-expired
// bypass cookie whose MAC verifies against the supplied secret. All
// comparisons are constant-time. Any failure is silently treated as "no
// valid cookie". The middleware MUST NOT leak diagnostic information
// about why a cookie was rejected.
func hasValidBypassCookie(r *http.Request, secret string) bool {
	if secret == "" {
		return false
	}
	cookie, err := r.Cookie(maintenanceBypassCookie)
	if err != nil || cookie == nil || cookie.Value == "" {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return false
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 {
		return false
	}
	expires, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return false
	}
	providedMAC, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}
	macKey, err := deriveMaintenanceMACKey(secret)
	if err != nil {
		return false
	}
	expectedMAC := computeMaintenanceMAC(macKey, expires)
	if subtle.ConstantTimeCompare(providedMAC, expectedMAC) != 1 {
		return false
	}
	// Reject after MAC verification so timing of the expiry check cannot
	// be used to probe expiry separately from validity.
	if time.Now().Unix() >= expires {
		return false
	}
	return true
}

// shouldUseSecureBypassCookie reports whether the bypass cookie should set
// the Secure attribute. Defaults to true; relaxed only when APP_ENV is
// "development" or "testing" to keep local HTTP flows usable.
func shouldUseSecureBypassCookie() bool {
	switch strings.ToLower(os.Getenv("APP_ENV")) {
	case "development", "dev", "testing", "test":
		return false
	default:
		return true
	}
}
