package store

import (
	"errors"
	"testing"
)

func TestSentinelsAreDistinct(t *testing.T) {
	if errors.Is(ErrNotFound, ErrConflict) {
		t.Fatal("ErrNotFound and ErrConflict must be distinct sentinels")
	}
	if ErrNotFound == nil || ErrConflict == nil {
		t.Fatal("sentinels must be non-nil")
	}
}
