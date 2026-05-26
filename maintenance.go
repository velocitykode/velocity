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

// PreventRequestsDuringMaintenance returns middleware that returns a 503
// Service Unavailable response while the application is in maintenance mode.
//
// If the down-file contains a secret and the request path matches "/" + secret,
// the middleware mints a signed bypass cookie and redirects to "/". Subsequent
// requests carrying a valid, non-expired bypass cookie are served normally.
// The bypass MAC is keyed off the operator-supplied secret via HKDF so leaking
// APP_KEY alone cannot grant a bypass.
func PreventRequestsDuringMaintenance() router.MiddlewareFunc {
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
