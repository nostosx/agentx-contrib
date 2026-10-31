package browser

import "testing"

func TestOptionsRejectMissingExtractorAndLimits(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("missing extractor accepted")
	}
}
