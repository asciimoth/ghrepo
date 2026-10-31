// Package storage defines an optional owned-asset backend.
package storage

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
)

var assetNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Backend stores immutable package bytes and returns a public HTTPS URL.
type Backend interface {
	Put(context.Context, string, []byte) (string, error)
	Get(context.Context, string) ([]byte, error)
}

// Memory is an offline test backend. The URL is only an identifier.
type Memory struct {
	mu    sync.Mutex
	items map[string][]byte
}

func NewMemory() *Memory { return &Memory{items: map[string][]byte{}} }

func (m *Memory) Put(ctx context.Context, name string, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !assetNameRE.MatchString(name) || strings.Contains(name, "..") {
		return "", errors.New("invalid asset name")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.items[name]; ok && string(old) != string(data) {
		return "", errors.New("asset name already contains different bytes")
	}
	m.items[name] = append([]byte(nil), data...)
	return "https://memory.invalid/" + name, nil
}

func (m *Memory) Get(ctx context.Context, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.items[name]
	if !ok {
		return nil, errors.New("asset not found")
	}
	return append([]byte(nil), data...), nil
}
