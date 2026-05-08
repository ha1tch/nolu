// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package tenant

import (
	"testing"
)

// ---------------------------------------------------------------------------
// GraphNodePrefix
// ---------------------------------------------------------------------------

func TestGraphNodePrefix(t *testing.T) {
	cases := []struct {
		id   uint16
		want string
	}{
		{0, ""},         // zero tenant: empty (single-tenant fallback)
		{1, "0001@"},
		{2, "0002@"},
		{255, "00FF@"},
		{256, "0100@"},
		{4096, "1000@"},
		{0xFFFF, "FFFF@"}, // boundary: maximum uint16
	}
	for _, c := range cases {
		got := GraphNodePrefix(c.id)
		if got != c.want {
			t.Errorf("GraphNodePrefix(%d): want %q, got %q", c.id, c.want, got)
		}
	}
}

func TestGraphNodePrefix_NonZeroIsExactly5Chars(t *testing.T) {
	// The @ terminator at position 4 is load-bearing: it prevents one prefix
	// from being a prefix-match of another (e.g. "0001@" vs "00010@").
	for _, id := range []uint16{1, 10, 100, 1000, 10000, 0xFFFE, 0xFFFF} {
		p := GraphNodePrefix(id)
		if len(p) != 5 {
			t.Errorf("GraphNodePrefix(%d) = %q: want length 5, got %d", id, p, len(p))
		}
		if p[4] != '@' {
			t.Errorf("GraphNodePrefix(%d) = %q: want '@' at position 4", id, p)
		}
	}
}

func TestGraphNodePrefix_UsesUppercaseHex(t *testing.T) {
	// Must be uppercase to match what NodeIDPrefix recognises.
	for _, id := range []uint16{0xA, 0xAB, 0xABC, 0xABCD} {
		p := GraphNodePrefix(id)
		for i, c := range p[:4] {
			if c >= 'a' && c <= 'f' {
				t.Errorf("GraphNodePrefix(%d) = %q: lowercase hex at position %d", id, p, i)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// NodeIDPrefix
// ---------------------------------------------------------------------------

func TestNodeIDPrefix(t *testing.T) {
	cases := []struct {
		nodeID string
		want   string
	}{
		{"0001@post:1", "0001@"},
		{"FFFF@doc:1", "FFFF@"},
		{"0000@x:1", "0000@"},
		{"post:1", ""},        // no prefix
		{"001@post:1", ""},    // only 3 hex digits
		{"0001post:1", ""},    // missing @
		{"000G@post:1", ""},   // G is not valid hex
		{"000g@post:1", ""},   // lowercase g — not recognised
		{"0001@", "0001@"},    // prefix only, no node payload
		{"", ""},
	}
	for _, c := range cases {
		got := NodeIDPrefix(c.nodeID)
		if got != c.want {
			t.Errorf("NodeIDPrefix(%q): want %q, got %q", c.nodeID, c.want, got)
		}
	}
}

func TestNodeIDPrefix_RoundTripWithGraphNodePrefix(t *testing.T) {
	// For any non-zero tenant, NodeIDPrefix(GraphNodePrefix(id) + "entity:1")
	// must return exactly GraphNodePrefix(id).
	for _, id := range []uint16{1, 255, 256, 0xABCD, 0xFFFF} {
		prefix := GraphNodePrefix(id)
		nodeID := prefix + "entity:1"
		got := NodeIDPrefix(nodeID)
		if got != prefix {
			t.Errorf("round-trip for id %d: GraphNodePrefix=%q, NodeIDPrefix(%q)=%q",
				id, prefix, nodeID, got)
		}
	}
}

func TestNodeIDPrefix_LowercaseNotRecognised(t *testing.T) {
	// Lowercase prefixes are not produced by GraphNodePrefix, so they must
	// not be recognised. This prevents ambiguous or externally-crafted IDs
	// from bypassing the cross-tenant edge guard.
	lowercasePrefixed := []string{
		"000a@post:1",
		"00ff@doc:1",
		"abcd@item:1",
	}
	for _, n := range lowercasePrefixed {
		if got := NodeIDPrefix(n); got != "" {
			t.Errorf("NodeIDPrefix(%q): expected \"\", got %q (lowercase must not be recognised)", n, got)
		}
	}
}

// ---------------------------------------------------------------------------
// StorageDirSegment
// ---------------------------------------------------------------------------

func TestStorageDirSegment(t *testing.T) {
	cases := []struct {
		id   uint16
		want string
	}{
		{0, ""},        // zero: no segment
		{1, "t0001"},
		{255, "t00FF"},
		{256, "t0100"},
		{0xFFFF, "tFFFF"},
	}
	for _, c := range cases {
		got := StorageDirSegment(c.id)
		if got != c.want {
			t.Errorf("StorageDirSegment(%d): want %q, got %q", c.id, c.want, got)
		}
	}
}

func TestStorageDirSegment_UsesUppercaseHex(t *testing.T) {
	for _, id := range []uint16{0xA, 0xAB, 0xABC, 0xABCD} {
		seg := StorageDirSegment(id)
		if len(seg) < 2 {
			t.Errorf("StorageDirSegment(%d) = %q: too short", id, seg)
			continue
		}
		for i, c := range seg[1:] { // skip leading 't'
			if c >= 'a' && c <= 'f' {
				t.Errorf("StorageDirSegment(%d) = %q: lowercase hex at position %d", id, seg, i+1)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// GraphEdgesTableName
// ---------------------------------------------------------------------------

func TestGraphEdgesTableName(t *testing.T) {
	cases := []struct {
		id   uint16
		want string
	}{
		{0, "graph_t0000"},
		{1, "graph_t0001"},
		{255, "graph_t00FF"},
		{256, "graph_t0100"},
		{0xFFFF, "graph_tFFFF"},
	}
	for _, c := range cases {
		got := GraphEdgesTableName(c.id)
		if got != c.want {
			t.Errorf("GraphEdgesTableName(%d): want %q, got %q", c.id, c.want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// ScopeKey
// ---------------------------------------------------------------------------

func TestScopeKey(t *testing.T) {
	cases := []struct {
		id   uint16
		key  string
		want string
	}{
		{0, "post:list:1:10", "post:list:1:10"},    // zero: unchanged
		{1, "post:list:1:10", "0001:post:list:1:10"},
		{0xFFFF, "x", "FFFF:x"},
		{0, "", ""},
		{1, "", "0001:"},
	}
	for _, c := range cases {
		got := ScopeKey(c.id, c.key)
		if got != c.want {
			t.Errorf("ScopeKey(%d, %q): want %q, got %q", c.id, c.key, c.want, got)
		}
	}
}
