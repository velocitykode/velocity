package rules

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
)

// FileHeader is the minimum shape we care about for file-related rules.
// In practice the concrete type is *multipart.FileHeader, but we also
// accept anything that carries a Filename string and a Size int64 - this
// lets tests pass in a lightweight fake without constructing a real
// multipart form.
//
// MimesRule and ImageRule additionally require the value to be openable
// (either a *multipart.FileHeader, or any type implementing
// Open() (multipart.File, error)) so they can sniff the first 512 bytes
// and verify the content matches the requested extension. Filename
// extension alone is not trustworthy when validating user uploads.
type FileHeader interface {
	// Filename returns the original filename the client supplied.
	Filename() string
	// Size returns the file size in bytes.
	Size() int64
}

// openable describes a value that can produce a fresh reader over its
// content. *multipart.FileHeader satisfies it natively (each Open returns
// an independent multipart.File). Tests and adopters can implement it on
// their own FileHeader-shaped types.
type openable interface {
	Open() (multipart.File, error)
}

// imageExtensions lists the extensions ImageRule recognises by filename.
// Content sniffing still runs on top of the extension check.
//
// SVG is intentionally excluded by default: SVG is XML and can carry
// <script> blocks, so accepting it without explicit opt-in is unsafe.
// Pass image:svg (or image:allow_svg) to the rule to permit SVG uploads.
var imageExtensions = map[string]struct{}{
	".jpg":  {},
	".jpeg": {},
	".png":  {},
	".gif":  {},
	".webp": {},
	".bmp":  {},
	".heic": {},
	".heif": {},
	".avif": {},
}

// imageMimePrefixes is the set of MIME prefixes ImageRule accepts when
// sniffing the first 512 bytes of an upload. http.DetectContentType
// returns "image/jpeg", "image/png", etc. We compare against this set
// to confirm the extension matches the actual content.
var imageMimePrefixes = []string{
	"image/jpeg",
	"image/png",
	"image/gif",
	"image/webp",
	"image/bmp",
	"image/heic",
	"image/heif",
	"image/avif",
}

// extMimeAllowlist maps short MIME names (jpg, png, pdf, ...) to the
// content types that http.DetectContentType is allowed to return for
// uploads claiming that extension. Used by MimesRule to confirm the
// client-supplied filename matches actual file content.
//
// Each value is a list of prefixes; a sniffed type matches if it starts
// with one of the listed prefixes. Trailing parameters such as
// "; charset=utf-8" are therefore accepted.
var extMimeAllowlist = map[string][]string{
	"jpg":  {"image/jpeg"},
	"jpeg": {"image/jpeg"},
	"png":  {"image/png"},
	"gif":  {"image/gif"},
	"webp": {"image/webp"},
	"bmp":  {"image/bmp"},
	"heic": {"image/heic", "image/heif"},
	"heif": {"image/heif", "image/heic"},
	"avif": {"image/avif"},
	"pdf":  {"application/pdf"},
	"txt":  {"text/plain"},
	"csv":  {"text/csv", "text/plain"},
	"html": {"text/html"},
	"htm":  {"text/html"},
	"json": {"application/json", "text/plain"},
	"xml":  {"text/xml", "application/xml", "text/plain"},
	"zip":  {"application/zip", "application/x-zip-compressed"},
	"gz":   {"application/x-gzip", "application/gzip"},
	"tar":  {"application/x-tar"},
	"mp3":  {"audio/mpeg"},
	"mp4":  {"video/mp4", "audio/mp4"},
	"webm": {"video/webm", "audio/webm"},
	"ogg":  {"audio/ogg", "application/ogg", "video/ogg"},
	"wav":  {"audio/wave", "audio/wav", "audio/x-wav"},
	"svg":  {"text/xml", "image/svg+xml", "text/plain"},
}

// blockedExecutableExts is the set of script-runner / executable suffixes
// that MimesRule refuses outright, regardless of the configured allowlist.
// These extensions cause RCE when the upload is later served by a webserver
// that runs them, and there is rarely a legitimate reason to permit them
// through a "mimes:" content-type gate.
var blockedExecutableExts = map[string]struct{}{
	".php":      {},
	".phtml":    {},
	".phar":     {},
	".php3":     {},
	".php4":     {},
	".php5":     {},
	".php7":     {},
	".php8":     {},
	".cgi":      {},
	".pl":       {},
	".py":       {},
	".rb":       {},
	".sh":       {},
	".bash":     {},
	".exe":      {},
	".bat":      {},
	".cmd":      {},
	".htaccess": {},
}

// fileLike extracts FileHeader-compatible metadata from common shapes.
// The opener return is nil when the value carries no readable content,
// in which case rules that require content sniffing must fail closed.
func fileLike(v interface{}) (name string, size int64, opener openable, ok bool) {
	switch f := v.(type) {
	case nil:
		return "", 0, nil, false
	case *multipart.FileHeader:
		if f == nil {
			return "", 0, nil, false
		}
		return f.Filename, f.Size, f, true
	case multipart.FileHeader:
		// Take address so the openable method set is reachable.
		fh := f
		return fh.Filename, fh.Size, &fh, true
	case FileHeader:
		if f == nil {
			return "", 0, nil, false
		}
		o, _ := f.(openable)
		return f.Filename(), f.Size(), o, true
	}
	return "", 0, nil, false
}

// sniffContentType reads the first 512 bytes via opener.Open and returns
// the http.DetectContentType result. Each Open returns an independent
// reader, so reading here does not consume bytes from later consumers
// who reopen the file.
func sniffContentType(o openable) (string, error) {
	if o == nil {
		return "", errors.New("file content is not readable for sniffing")
	}
	f, err := o.Open()
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return "", err
	}
	return http.DetectContentType(buf[:n]), nil
}

// mimeMatches reports whether sniffed starts with any of the allowed
// prefixes. The sniffed value may carry "; charset=..." trailers, so we
// match by prefix rather than equality.
func mimeMatches(sniffed string, allowed []string) bool {
	for _, a := range allowed {
		if strings.HasPrefix(sniffed, a) {
			return true
		}
	}
	return false
}

// FileRule validates that a value carries file-upload metadata
// (a *multipart.FileHeader or equivalent).
//
// Note: This rule is a shape check on the value stored in the validation
// data map. Velocity's ExtractRequestData currently does not populate
// multipart files; handlers that need file validation should merge the
// *multipart.FileHeader into the data map before calling Check.
func FileRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}
	if _, _, _, ok := fileLike(value); !ok {
		return fmt.Errorf("The %s field must be a file.", field)
	}
	return nil
}

// MimesRule validates that an uploaded file's extension matches one of
// the whitelisted short mime names AND that the file's actual content
// (first 512 bytes, http.DetectContentType) matches that extension.
// Usage: mimes:jpg,png,pdf
//
// Script-runner extensions (.php, .phtml, .phar, .cgi, .pl, .py, .rb,
// .sh, ...) are refused outright regardless of the parameter list.
//
// SVG handling: "svg" in the parameter list is refused unless the
// caller also passes the literal token "allow_svg" (e.g.
// mimes:svg,allow_svg or mimes:jpg,svg,allow_svg). Even with opt-in,
// SVG content containing a <script tag is refused; see validateSVG.
func MimesRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}
	if len(params) < 1 {
		return fmt.Errorf("The mimes rule requires at least 1 parameter.")
	}
	name, _, opener, ok := fileLike(value)
	if !ok {
		return fmt.Errorf("The %s field must be a file.", field)
	}
	lowerName := strings.ToLower(name)
	dotExt := filepath.Ext(lowerName)
	ext := strings.TrimPrefix(dotExt, ".")

	// Refuse known script-runner extensions unconditionally. An adopter
	// who needs to ship .php uploads through "mimes:" should re-think
	// the deployment story rather than override this list.
	if _, blocked := blockedExecutableExts[dotExt]; blocked {
		return fmt.Errorf("The %s field has a disallowed file type.", field)
	}
	// Also refuse if any earlier segment of the filename matches a
	// blocked extension (e.g. payload.php.jpg). Apache and some legacy
	// webservers resolve such names to the first runnable handler.
	for _, seg := range strings.Split(lowerName, ".") {
		if _, blocked := blockedExecutableExts["."+seg]; blocked {
			return fmt.Errorf("The %s field has a disallowed file type.", field)
		}
	}

	// Parse params: "allow_svg" is a flag, everything else is an
	// extension allowlist entry.
	allowSVG := false
	hasSVGToken := false
	matched := false
	for _, p := range params {
		token := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(p)), ".")
		if token == "allow_svg" {
			allowSVG = true
			continue
		}
		if token == "svg" {
			hasSVGToken = true
		}
		if token == ext {
			matched = true
		}
	}
	// "svg" in the allowlist is rejected unless the caller also passes
	// "allow_svg". Without opt-in we never let an SVG upload through
	// MimesRule, even when the filename matches.
	if hasSVGToken && !allowSVG {
		return fmt.Errorf("The %s field must be a file of type: %s.", field, strings.Join(params, ", "))
	}
	if !matched {
		return fmt.Errorf("The %s field must be a file of type: %s.", field, strings.Join(params, ", "))
	}

	// Then sniff content and confirm the actual bytes match the
	// extension. Fail closed when the value has no opener: without
	// content the extension alone cannot be trusted.
	if opener == nil {
		return fmt.Errorf("The %s field could not be verified.", field)
	}
	sniffed, err := sniffContentType(opener)
	if err != nil {
		return fmt.Errorf("The %s field could not be verified.", field)
	}
	allowed, known := extMimeAllowlist[ext]
	if !known {
		// Extension is in the caller's allowlist but we have no MIME
		// mapping. Refuse rather than silently pass.
		return fmt.Errorf("The %s field has an unsupported file type.", field)
	}
	if !mimeMatches(sniffed, allowed) {
		return fmt.Errorf("The %s field must be a file of type: %s.", field, strings.Join(params, ", "))
	}

	// SVG uploads still need the script-tag rejection check even when
	// the caller opted in. Defense in depth against the most common
	// SVG XSS payload; deeper sanitization is the adopter's job.
	if ext == "svg" {
		if err := validateSVG(opener); err != nil {
			return fmt.Errorf("The %s field must be a file of type: %s.", field, strings.Join(params, ", "))
		}
	}
	return nil
}

// ImageRule validates that an uploaded file is an image, checking both
// the filename extension and the actual content (via http.DetectContentType
// on the first 512 bytes). SVG is excluded by default because it can carry
// <script> blocks; pass image:svg (or image:allow_svg) to opt in.
func ImageRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}
	name, _, opener, ok := fileLike(value)
	if !ok {
		return fmt.Errorf("The %s field must be an image.", field)
	}
	allowSVG := false
	for _, p := range params {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "svg", "allow_svg":
			allowSVG = true
		}
	}
	ext := strings.ToLower(filepath.Ext(name))
	if _, known := imageExtensions[ext]; !known {
		if !(allowSVG && ext == ".svg") {
			return fmt.Errorf("The %s field must be an image.", field)
		}
	}

	// Sniff content. Fail closed when the value has no opener.
	if opener == nil {
		return fmt.Errorf("The %s field could not be verified as an image.", field)
	}
	sniffed, err := sniffContentType(opener)
	if err != nil {
		return fmt.Errorf("The %s field could not be verified as an image.", field)
	}
	if ext == ".svg" {
		if !allowSVG {
			return fmt.Errorf("The %s field must be an image.", field)
		}
		// SVG sniffs as XML or text/plain; accept those.
		if !mimeMatches(sniffed, []string{"image/svg+xml", "text/xml", "application/xml", "text/plain"}) {
			return fmt.Errorf("The %s field must be an image.", field)
		}
		// Refuse SVG content that carries a <script> block even when
		// the caller opted in. This is a baseline defense against the
		// most common SVG XSS payload; deeper sanitization is the
		// adopter's responsibility.
		if err := validateSVG(opener); err != nil {
			return fmt.Errorf("The %s field must be an image.", field)
		}
		return nil
	}
	if !mimeMatches(sniffed, imageMimePrefixes) {
		return fmt.Errorf("The %s field must be an image.", field)
	}
	return nil
}

// validateSVG reopens the file and refuses content that contains a
// <script tag (case-insensitive). Returns nil if no script tag is
// found. SVG files are typically small; we cap the read at 1MB.
//
// Shared between MimesRule and ImageRule so both rules apply the same
// SVG XSS check.
func validateSVG(o openable) error {
	f, err := o.Open()
	if err != nil {
		return err
	}
	defer f.Close()
	buf, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return err
	}
	lower := strings.ToLower(string(buf))
	if strings.Contains(lower, "<script") {
		return errors.New("svg contains script tag")
	}
	return nil
}
