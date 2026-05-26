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
//
// Some legitimate formats do not have a magic-byte signature that Go's
// http.DetectContentType recognises (tar, 7z, legacy MS Office, many MP4
// variants, ...). For those extensions we accept application/octet-stream
// as a fallback, leaning on the blockedExecutableExts list and the
// filename allowlist for safety. This is a deliberate trade-off
// documented per-row below.
//
// OOXML formats (docx, xlsx, pptx, odt, ods, odp, jar) are ZIP
// containers and sniff as application/zip. They are accepted as zip;
// the extension cross-check is what distinguishes them.
var extMimeAllowlist = map[string][]string{
	// Images.
	"jpg":  {"image/jpeg"},
	"jpeg": {"image/jpeg"},
	"png":  {"image/png"},
	"gif":  {"image/gif"},
	"webp": {"image/webp"},
	"bmp":  {"image/bmp"},
	"heic": {"image/heic", "image/heif"},
	"heif": {"image/heif", "image/heic"},
	"avif": {"image/avif"},
	"svg":  {"text/xml", "image/svg+xml", "text/plain"},
	"ico":  {"image/x-icon", "image/vnd.microsoft.icon", "application/octet-stream"},
	"tif":  {"image/tiff", "application/octet-stream"},
	"tiff": {"image/tiff", "application/octet-stream"},

	// Documents.
	"pdf": {"application/pdf"},
	"rtf": {"text/rtf", "application/rtf", "text/plain"},
	"txt": {"text/plain"},
	"csv": {"text/csv", "text/plain"},
	"tsv": {"text/tab-separated-values", "text/plain"},
	"md":  {"text/plain", "text/markdown"},

	// Web formats.
	"html": {"text/html"},
	"htm":  {"text/html"},
	"json": {"application/json", "text/plain"},
	"xml":  {"text/xml", "application/xml", "text/plain"},
	"yaml": {"text/plain", "application/yaml", "text/yaml"},
	"yml":  {"text/plain", "application/yaml", "text/yaml"},

	// OOXML / OpenDocument document containers (all ZIP-based; Go
	// sniffs them as application/zip and the cross-check is the
	// extension allowlist).
	//
	// Java archives (.jar) are deliberately NOT listed: a JAR is a ZIP
	// of executable Java bytecode, which violates the framework's
	// posture of "no executable uploads through mimes". Operators who
	// genuinely need JAR uploads should bypass mimes for that field
	// or wait for a dedicated archive-content allowlist rule (future
	// work).
	"docx": {"application/zip", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
	"xlsx": {"application/zip", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
	"pptx": {"application/zip", "application/vnd.openxmlformats-officedocument.presentationml.presentation"},
	"odt":  {"application/zip", "application/vnd.oasis.opendocument.text"},
	"ods":  {"application/zip", "application/vnd.oasis.opendocument.spreadsheet"},
	"odp":  {"application/zip", "application/vnd.oasis.opendocument.presentation"},

	// Legacy Microsoft Office binary formats use OLE compound storage,
	// which Go does not have a dedicated signature for. They sniff as
	// application/octet-stream; we accept that fallback and rely on the
	// .doc/.xls/.ppt extension plus the executable blocklist.
	"doc": {"application/msword", "application/octet-stream"},
	"xls": {"application/vnd.ms-excel", "application/octet-stream"},
	"ppt": {"application/vnd.ms-powerpoint", "application/octet-stream"},

	// Archives. tar and 7z lack reliable magic-byte signatures in Go's
	// sniffer; we accept application/octet-stream alongside the
	// canonical type.
	"zip": {"application/zip", "application/x-zip-compressed"},
	"gz":  {"application/x-gzip", "application/gzip"},
	"tar": {"application/x-tar", "application/octet-stream"},
	"rar": {"application/x-rar-compressed", "application/vnd.rar", "application/octet-stream"},
	"7z":  {"application/x-7z-compressed", "application/octet-stream"},

	// Audio / video. mp4 and some other media containers do not always
	// sniff to the expected MIME; we accept application/octet-stream as
	// a fallback.
	"mp3":  {"audio/mpeg"},
	"mp4":  {"video/mp4", "audio/mp4", "application/mp4", "application/octet-stream"},
	"m4a":  {"audio/mp4", "audio/x-m4a", "application/octet-stream"},
	"webm": {"video/webm", "audio/webm"},
	"ogg":  {"audio/ogg", "application/ogg", "video/ogg"},
	"oga":  {"audio/ogg", "application/ogg"},
	"ogv":  {"video/ogg", "application/ogg"},
	"wav":  {"audio/wave", "audio/wav", "audio/x-wav"},
	"flac": {"audio/flac", "audio/x-flac", "application/octet-stream"},
	"aac":  {"audio/aac", "audio/x-aac", "application/octet-stream"},

	// WebAssembly. Go 1.21+ recognises the \x00asm signature.
	"wasm": {"application/wasm", "application/octet-stream"},
}

// blockedExecutableExts is the set of script-runner / executable suffixes
// that MimesRule refuses outright, regardless of the configured allowlist.
// These extensions cause RCE when the upload is later served by a webserver
// that runs them, and there is rarely a legitimate reason to permit them
// through a "mimes:" content-type gate.
//
// .jar is included even though a JAR file sniffs as application/zip:
// JARs bundle executable Java bytecode and a host that imports them
// will run that code. Refusing the extension closes the rename vector
// (foo.jar uploaded under mimes:zip) and matches the explicit removal
// of .jar from extMimeAllowlist.
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
	".jar":      {},
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

// sniffHead reads the first 512 bytes via opener.Open and returns the
// raw bytes. Used by sniffContentType (for http.DetectContentType) and
// by isExecutableContent (for magic-byte refusal of PE/ELF/Mach-O
// payloads regardless of the claimed extension). Each Open returns an
// independent reader so re-sniffing later does not consume bytes from
// downstream consumers.
func sniffHead(o openable) ([]byte, error) {
	if o == nil {
		return nil, errors.New("file content is not readable for sniffing")
	}
	f, err := o.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	return buf[:n], nil
}

// sniffContentType returns the http.DetectContentType result for the
// first 512 bytes of the upload.
func sniffContentType(o openable) (string, error) {
	buf, err := sniffHead(o)
	if err != nil {
		return "", err
	}
	return http.DetectContentType(buf), nil
}

// isExecutableContent reports whether head begins with magic bytes that
// identify a native executable or compiled bytecode payload. These
// formats execute on a target system when launched and have no
// legitimate place in a "mimes:" content gate, regardless of what
// extension or sniffed MIME the rest of the rule would otherwise
// accept (e.g. the application/octet-stream fallback for opaque
// archive / OLE / older MP4 formats).
//
// Covered:
//   - PE/COFF (Windows .exe/.dll): "MZ" at offset 0.
//   - ELF (Linux/BSD/Solaris executables, .so): "\x7fELF" at offset 0.
//   - Mach-O 32-bit BE/LE: 0xFEEDFACE / 0xCEFAEDFE.
//   - Mach-O 64-bit BE/LE: 0xFEEDFACF / 0xCFFAEDFE.
//   - Mach-O fat / Java .class: 0xCAFEBABE. Java .class shares this
//     magic; we refuse both.
func isExecutableContent(head []byte) bool {
	// PE/COFF. "MZ" is sufficient on its own.
	if len(head) >= 2 && head[0] == 0x4D && head[1] == 0x5A {
		return true
	}
	if len(head) < 4 {
		return false
	}
	// ELF.
	if head[0] == 0x7F && head[1] == 0x45 && head[2] == 0x4C && head[3] == 0x46 {
		return true
	}
	// Mach-O 32-bit BE, 32-bit LE, 64-bit BE, 64-bit LE.
	if head[0] == 0xFE && head[1] == 0xED && head[2] == 0xFA && (head[3] == 0xCE || head[3] == 0xCF) {
		return true
	}
	if (head[0] == 0xCE || head[0] == 0xCF) && head[1] == 0xFA && head[2] == 0xED && head[3] == 0xFE {
		return true
	}
	// Mach-O fat / Java .class.
	if head[0] == 0xCA && head[1] == 0xFE && head[2] == 0xBA && head[3] == 0xBE {
		return true
	}
	return false
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
// Content-level executable refusal: uploads beginning with PE/COFF,
// ELF, Mach-O, or Java .class magic bytes are refused regardless of
// the claimed extension or sniffed MIME. This blocks renamed binary
// payloads (e.g. report.doc carrying a PE executable) that would
// otherwise slip past the application/octet-stream fallback used for
// opaque container formats.
//
// Archive-content limitation: mimes:zip accepts the ZIP container but
// does not scan its entries. A ZIP can carry arbitrary contents,
// including executable bytecode. The .jar extension is therefore
// refused outright (see blockedExecutableExts) and removed from
// extMimeAllowlist, but mimes:zip on a payload-bearing ZIP is still
// accepted. Adopters who need per-entry restrictions on archives
// should validate the archive contents separately after the mimes
// check passes.
//
// SVG handling: when the uploaded file itself is SVG (by extension),
// the caller must also pass the literal token "allow_svg" (e.g.
// mimes:svg,allow_svg or mimes:jpg,svg,allow_svg). Including "svg" in
// the allowlist alongside other extensions does NOT block non-SVG
// uploads from passing; mimes:jpg,svg with a clean .jpg is accepted
// without opt-in. Even with opt-in, SVG content containing a <script
// tag is refused; see validateSVG.
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
	matched := false
	for _, p := range params {
		token := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(p)), ".")
		if token == "allow_svg" {
			allowSVG = true
			continue
		}
		if token == ext {
			matched = true
		}
	}
	if !matched {
		return fmt.Errorf("The %s field must be a file of type: %s.", field, strings.Join(params, ", "))
	}

	// SVG-specific opt-in: only the uploaded file being SVG triggers
	// the allow_svg requirement. mimes:jpg,svg without allow_svg must
	// still accept clean .jpg uploads; the allow_svg flag is only
	// checked when the actual upload sniffs/names as .svg.
	if ext == "svg" && !allowSVG {
		return fmt.Errorf("The %s field must be a file of type: %s.", field, strings.Join(params, ", "))
	}

	// Then sniff content and confirm the actual bytes match the
	// extension. Fail closed when the value has no opener: without
	// content the extension alone cannot be trusted.
	if opener == nil {
		return fmt.Errorf("The %s field could not be verified.", field)
	}
	head, err := sniffHead(opener)
	if err != nil {
		return fmt.Errorf("The %s field could not be verified.", field)
	}
	// Content-level executable refusal: PE/ELF/Mach-O/Java-class
	// payloads are rejected regardless of the claimed extension or the
	// sniffed MIME. This closes the application/octet-stream fallback
	// hole where a renamed .exe would otherwise pass mimes:doc (sniff
	// returns octet-stream, allowlist accepts octet-stream for doc).
	if isExecutableContent(head) {
		return fmt.Errorf("The %s field has a disallowed file type.", field)
	}
	sniffed := http.DetectContentType(head)
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

// svgScanCap is the maximum size of an SVG upload that validateSVG will
// scan in full. SVG files are typically small (icons, logos); anything
// larger is refused fail-closed rather than scanned partially, because a
// partial scan can be bypassed by prepending padding/comments before the
// <script tag and pushing the payload past the cap.
const svgScanCap = 1 << 20

// validateSVG reopens the file and refuses content that contains a
// <script tag (case-insensitive). Returns nil if no script tag is
// found. Uploads larger than svgScanCap are refused outright (fail
// closed) since scanning only the first svgScanCap bytes lets an
// attacker hide the payload past that mark.
//
// Shared between MimesRule and ImageRule so both rules apply the same
// SVG XSS check.
func validateSVG(o openable) error {
	f, err := o.Open()
	if err != nil {
		return err
	}
	defer f.Close()
	// Read one byte past the cap so we can distinguish "fits within the
	// cap" from "too large to scan". A read that returns exactly
	// svgScanCap+1 bytes means the file exceeded the cap.
	buf, err := io.ReadAll(io.LimitReader(f, svgScanCap+1))
	if err != nil {
		return err
	}
	if int64(len(buf)) > svgScanCap {
		return errors.New("svg exceeds scan size limit")
	}
	lower := strings.ToLower(string(buf))
	if strings.Contains(lower, "<script") {
		return errors.New("svg contains script tag")
	}
	return nil
}
