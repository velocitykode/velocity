package s3

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// TestIsNotFoundError pins the typed not-found detection. The previous
// implementation string-matched "NotFound"/"NoSuchKey" anywhere in the
// error text, which would misclassify unrelated errors (e.g. a
// "PageNotFound" message) as object-missing. The typed version only
// recognises the concrete SDK error types and the smithy API error code.
func TestIsNotFoundError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"NoSuchKey typed", &types.NoSuchKey{}, true},
		{"NotFound typed", &types.NotFound{}, true},
		{"wrapped NoSuchKey", fmt.Errorf("get object: %w", &types.NoSuchKey{}), true},
		{"smithy code NotFound", &smithy.GenericAPIError{Code: "NotFound"}, true},
		{"smithy code NoSuchKey", &smithy.GenericAPIError{Code: "NoSuchKey"}, true},
		{"smithy other code", &smithy.GenericAPIError{Code: "AccessDenied"}, false},
		{"unrelated error", errors.New("connection reset"), false},
		// Regression: text contains "NotFound" but is not a typed S3 error.
		{"false positive text", errors.New("PageNotFound while rendering"), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNotFoundError(tc.err); got != tc.want {
				t.Errorf("isNotFoundError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
