package s3

import (
	"net/url"
	"strings"
	"testing"
)

// TestS3URL_EscapesReservedCharacters_CustomURL pins FS-URL-ESCAPE for
// the S3 driver custom-URL branch.
func TestS3URL_EscapesReservedCharacters_CustomURL(t *testing.T) {
	driver := &S3Driver{
		bucket: "test-bucket",
		region: "us-east-1",
		url:    "https://cdn.example.com/assets",
	}

	keys := []string{
		"my doc?v=1.pdf",
		"my doc#section.pdf",
		"my doc?v=1#sec.pdf",
		"100%real.pdf",
		"folder/a b/file?q=1.txt",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			got := driver.URL(key)
			u, err := url.Parse(got)
			if err != nil {
				t.Fatalf("emitted URL %q is unparseable: %v", got, err)
			}
			if u.RawQuery != "" {
				t.Errorf("URL %q has RawQuery=%q; reserved chars leaked", got, u.RawQuery)
			}
			if u.Fragment != "" {
				t.Errorf("URL %q has Fragment=%q; reserved chars leaked", got, u.Fragment)
			}
			const base = "https://cdn.example.com/assets/"
			if !strings.HasPrefix(got, base) {
				t.Fatalf("URL %q missing prefix %q", got, base)
			}
			decoded, err := url.PathUnescape(strings.TrimPrefix(got, base))
			if err != nil {
				t.Fatalf("encoded segment failed to unescape: %v", err)
			}
			if decoded != key {
				t.Errorf("decoded %q != original %q", decoded, key)
			}
		})
	}
}

// TestS3URL_EscapesReservedCharacters_SynthesisedURL pins FS-URL-ESCAPE
// for the synthesised `s3.<region>.amazonaws.com` branch.
func TestS3URL_EscapesReservedCharacters_SynthesisedURL(t *testing.T) {
	driver := &S3Driver{
		bucket: "test-bucket",
		region: "us-east-1",
		url:    "",
	}

	got := driver.URL("my doc?v=1#sec.pdf")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("emitted URL %q is unparseable: %v", got, err)
	}
	if u.RawQuery != "" {
		t.Errorf("URL %q has RawQuery=%q; reserved chars leaked", got, u.RawQuery)
	}
	if u.Fragment != "" {
		t.Errorf("URL %q has Fragment=%q; reserved chars leaked", got, u.Fragment)
	}
	wantPrefix := "https://test-bucket.s3.us-east-1.amazonaws.com/"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("URL %q missing prefix %q", got, wantPrefix)
	}
	decoded, err := url.PathUnescape(strings.TrimPrefix(got, wantPrefix))
	if err != nil {
		t.Fatalf("encoded segment failed to unescape: %v", err)
	}
	if decoded != "my doc?v=1#sec.pdf" {
		t.Errorf("decoded %q != original", decoded)
	}
}
