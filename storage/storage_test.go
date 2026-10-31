package storage

import (
	"context"
	"testing"
)

func TestMemoryImmutableAndCopies(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	data := []byte("first")
	url, err := m.Put(ctx, "asset", data)
	if err != nil || url != "https://memory.invalid/asset" {
		t.Fatalf("put: %v", err)
	}
	data[0] = 'X'
	got, err := m.Get(ctx, "asset")
	if err != nil || string(got) != "first" {
		t.Fatalf("get: %v", err)
	}
	got[0] = 'Y'
	got, _ = m.Get(ctx, "asset")
	if string(got) != "first" {
		t.Fatal("get exposed mutable data")
	}
	if _, err := m.Put(ctx, "asset", []byte("second")); err == nil {
		t.Fatal("overwrote asset")
	}
	if _, err := m.Put(ctx, "asset", []byte("first")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "../asset", "asset?token=secret"} {
		if _, err := m.Put(ctx, name, []byte("first")); err == nil {
			t.Fatalf("accepted unsafe asset name %q", name)
		}
	}
}
