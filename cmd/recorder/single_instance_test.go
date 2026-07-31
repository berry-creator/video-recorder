package main

import "testing"

func TestInstanceGuardCloseIsIdempotent(t *testing.T) {
	releases := 0
	guard := newInstanceGuard(func() error {
		releases++
		return nil
	})
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	if releases != 1 {
		t.Fatalf("release calls = %d, want 1", releases)
	}
}
