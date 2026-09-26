package weverse

import (
	"errors"
	"fmt"
	"testing"
)

func TestReportPostUnavailableIncludesPasswordProtectedPosts(t *testing.T) {
	for _, err := range []error{ErrForbidden, ErrNotFound, ErrPostPassword, fmt.Errorf("wrapped: %w", ErrPostPassword)} {
		if !reportPostUnavailable(err) {
			t.Fatalf("expected unavailable classification for %v", err)
		}
	}
	if reportPostUnavailable(errors.New("network unavailable")) {
		t.Fatal("transient network errors must still fail the backfill")
	}
}
