package orm

import "testing"

// TestToSnakeCase covers acronym->word boundary handling. The original
// implementation collapsed all-caps runs into a single token (so "SSHKeyID"
// became "sshkey_id"). The fix inserts an underscore between an acronym and
// the following Word when the next char is lowercase.
func TestToSnakeCase(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// Acronym -> word boundary (the bug fix).
		{"SSHKeyID", "ssh_key_id"},
		{"URLPath", "url_path"},
		{"OAuthID", "o_auth_id"},
		{"HTTPSConnection", "https_connection"},

		// Existing behavior must keep working.
		{"ProviderID", "provider_id"},
		{"ID", "id"},
		{"userID", "user_id"},
		{"A", "a"},
		{"", ""},

		// A few extras worth pinning down.
		{"User", "user"},
		{"UserName", "user_name"},

		// Digit -> upper boundary (regression: previously these lost the
		// underscore after the fix for acronym boundaries).
		{"Field1Name", "field1_name"},
		{"OAuth2Token", "o_auth2_token"},
		{"Zone1AConfig", "zone1_a_config"},

		// Unicode: lowercase form must not be truncated to a single byte.
		// "Ä" lowercases to "ä" (two UTF-8 bytes), and the result must
		// preserve all of them. The leading "Ä" is not in [A-Z] so no
		// underscore is inserted before it; later ASCII uppercase letters
		// behave normally.
		{"ÄName", "äname"},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := ToSnakeCase(tc.in)
			if got != tc.want {
				t.Fatalf("ToSnakeCase(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}
