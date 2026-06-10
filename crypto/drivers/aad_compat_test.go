package drivers

import "testing"

// Pinned ciphertext fixtures captured from the pre-AAD CBC code (the
// plain EncryptBytes path and the hand-crafted v0 framing) under the
// fixed key below. They guard the V2-12 change: adding AAD support to
// CBC must not alter either non-AAD MAC framing, or every CBC
// ciphertext in the wild (cookies, signed URLs, encrypted DB columns)
// would stop decrypting. If one of these tests fails, the non-AAD
// framing drifted; fix the code, do NOT regenerate the fixture.
const (
	compatKey = "0123456789abcdef0123456789abcdef" // AES-256-CBC master key

	// EncryptBytes([]byte("legacy plain-CBC fixture")), captured pre-change.
	compatV1Plain     = "v1:eyJpdiI6ImJtSWNiazUyMCsrdjhmZmticnkrSnc9PSIsInZhbHVlIjoiQ25UQWNIaGVrYksranBqd1plaDV5UE5BdVAxUC9xdXFDbzFYWE9CSThSZz0iLCJtYWMiOiJjYWhvUlZYMnVOTVU5TGVqUnV4Nlp4c01ESFREVUJrM3RwaFV1V0xBTG9jPSJ9"
	compatV1Plaintext = "legacy plain-CBC fixture"

	// v0 (no sentinel, legacy fmt-concatenated MAC) fixture.
	compatV0          = "eyJpdiI6IklkcnlaQ1MyYk1tTTkxVU1qZkNnZ3c9PSIsInZhbHVlIjoiUVMybkYxb3J5MHJIZ2x6alZWcUFzMXZmV3FRSFVFSEEyU09vWXA1WE45bz0iLCJtYWMiOiJ1ekZwbUpDK1BIZ0xvcTRNRjVCek16cDNzMUZNKzNFODNhL3IwMEp5dnY0PSJ9"
	compatV0Plaintext = "legacy v0 fixture"
)

// TestCBCAADChange_LegacyBlobsStillDecrypt asserts pre-AAD CBC
// ciphertexts still decrypt through DecryptBytes after the AAD framing
// was added (V2-12 backward-compatibility requirement).
func TestCBCAADChange_LegacyBlobsStillDecrypt(t *testing.T) {
	d, err := NewAESDriver([]byte(compatKey), nil, "AES-256-CBC")
	if err != nil {
		t.Fatalf("NewAESDriver: %v", err)
	}

	got, err := d.Decrypt(compatV1Plain)
	if err != nil {
		t.Fatalf("pre-change v1 CBC fixture no longer decrypts: %v", err)
	}
	if got != compatV1Plaintext {
		t.Fatalf("v1 fixture plaintext = %q, want %q", got, compatV1Plaintext)
	}

	got, err = d.Decrypt(compatV0)
	if err != nil {
		t.Fatalf("pre-change v0 CBC fixture no longer decrypts: %v", err)
	}
	if got != compatV0Plaintext {
		t.Fatalf("v0 fixture plaintext = %q, want %q", got, compatV0Plaintext)
	}
}

// TestCBCAADChange_LegacyBlobRotatesThroughPreviousKeys asserts the
// PreviousKeys flow still reaches pre-AAD ciphertexts: a driver whose
// active key differs but carries the fixture's master in PreviousKeys
// must decrypt both wire versions.
func TestCBCAADChange_LegacyBlobRotatesThroughPreviousKeys(t *testing.T) {
	newMaster := make([]byte, 32)
	for i := range newMaster {
		newMaster[i] = byte(0xC3 ^ i)
	}
	d, err := NewAESDriver(newMaster, [][]byte{[]byte(compatKey)}, "AES-256-CBC")
	if err != nil {
		t.Fatalf("NewAESDriver: %v", err)
	}

	if got, err := d.Decrypt(compatV1Plain); err != nil || got != compatV1Plaintext {
		t.Fatalf("v1 fixture via PreviousKeys: got %q, err %v", got, err)
	}
	if got, err := d.Decrypt(compatV0); err != nil || got != compatV0Plaintext {
		t.Fatalf("v0 fixture via PreviousKeys: got %q, err %v", got, err)
	}
}
