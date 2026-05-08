// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// Unit tests for the pure-function prefix helpers in graph_tenant_handlers.go:
//   addPrefix, stripPrefix, stripPrefixFromEdgeMap, stripPrefixFromSlice
//
// tenant.GraphNodePrefix and tenant.NodeIDPrefix are tested in pkg/tenant.

package server

import (
	"testing"
)

// ---------------------------------------------------------------------------
// addPrefix
// ---------------------------------------------------------------------------

func TestAddPrefix(t *testing.T) {
	cases := []struct {
		prefix string
		nodeID string
		want   string
	}{
		{"0001@", "post:1", "0001@post:1"},
		{"0001@", "author:42", "0001@author:42"},
		{"FFFF@", "doc:1", "FFFF@doc:1"},
		// Empty prefix (tenant 0): must return node ID unchanged.
		{"", "post:1", "post:1"},
		{"", "author:99", "author:99"},
	}
	for _, c := range cases {
		got := addPrefix(c.prefix, c.nodeID)
		if got != c.want {
			t.Errorf("addPrefix(%q, %q): want %q, got %q", c.prefix, c.nodeID, c.want, got)
		}
	}
}

func TestAddPrefix_DoesNotDoublePrefix(t *testing.T) {
	// addPrefix is not idempotent — calling it twice would double the prefix.
	// This test documents that fact so callers know they must not call it twice.
	p := "0001@"
	node := "post:1"
	once := addPrefix(p, node)
	twice := addPrefix(p, once) // should be "0001@0001@post:1" — wrong
	if twice == "0001@post:1" {
		t.Error("addPrefix appears to be idempotent, which is not expected")
	}
	if twice != "0001@0001@post:1" {
		t.Errorf("addPrefix double: got %q, expected %q", twice, "0001@0001@post:1")
	}
}

// ---------------------------------------------------------------------------
// stripPrefix
// ---------------------------------------------------------------------------

func TestStripPrefix(t *testing.T) {
	cases := []struct {
		prefix string
		nodeID string
		want   string
	}{
		{"0001@", "0001@post:1", "post:1"},
		{"0001@", "0001@author:42", "author:42"},
		{"FFFF@", "FFFF@doc:1", "doc:1"},
		// Empty prefix: must return node ID unchanged.
		{"", "post:1", "post:1"},
		// Node ID that does not carry the prefix: returned unchanged.
		// (This would indicate a bug upstream, but the function must not panic.)
		{"0001@", "post:1", "post:1"},
		// Node ID carrying a different tenant's prefix: returned unchanged.
		{"0001@", "0002@post:1", "0002@post:1"},
	}
	for _, c := range cases {
		got := stripPrefix(c.prefix, c.nodeID)
		if got != c.want {
			t.Errorf("stripPrefix(%q, %q): want %q, got %q", c.prefix, c.nodeID, c.want, got)
		}
	}
}

func TestStripPrefix_RoundTrip(t *testing.T) {
	// addPrefix followed by stripPrefix must recover the original node ID.
	prefixes := []string{"0001@", "0002@", "FFFF@", ""}
	nodes := []string{"post:1", "author:42", "tag:99", "comment:1000"}
	for _, p := range prefixes {
		for _, n := range nodes {
			prefixed := addPrefix(p, n)
			recovered := stripPrefix(p, prefixed)
			if recovered != n {
				t.Errorf("round-trip(%q, %q): got %q", p, n, recovered)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// stripPrefixFromEdgeMap
// ---------------------------------------------------------------------------

func TestStripPrefixFromEdgeMap(t *testing.T) {
	prefix := "0001@"
	input := map[string]string{
		"0001@author:1": "author_ref",
		"0001@tag:1":    "tag_ref",
	}
	got := stripPrefixFromEdgeMap(prefix, input)

	if _, ok := got["author:1"]; !ok {
		t.Error("expected key \"author:1\" after stripping")
	}
	if _, ok := got["tag:1"]; !ok {
		t.Error("expected key \"tag:1\" after stripping")
	}
	if len(got) != 2 {
		t.Errorf("expected 2 entries, got %d: %v", len(got), got)
	}
	// Relationship values must be preserved exactly.
	if got["author:1"] != "author_ref" {
		t.Errorf("relationship value for author:1: want %q, got %q", "author_ref", got["author:1"])
	}
}

func TestStripPrefixFromEdgeMap_EmptyPrefix(t *testing.T) {
	input := map[string]string{
		"post:1": "next_ref",
	}
	got := stripPrefixFromEdgeMap("", input)
	// Empty prefix: must return the original map unmodified.
	// &got == &input (same map reference) is acceptable; no assertion needed.
	if v, ok := got["post:1"]; !ok || v != "next_ref" {
		t.Errorf("empty prefix: expected map unchanged, got %v", got)
	}
}

func TestStripPrefixFromEdgeMap_NilAndEmpty(t *testing.T) {
	// Must not panic on nil or empty map.
	if got := stripPrefixFromEdgeMap("0001@", nil); got != nil {
		t.Errorf("nil map: expected nil back, got %v", got)
	}
	empty := map[string]string{}
	if got := stripPrefixFromEdgeMap("0001@", empty); len(got) != 0 {
		t.Errorf("empty map: expected empty back, got %v", got)
	}
}

func TestStripPrefixFromEdgeMap_RelationshipTypeNotStripped(t *testing.T) {
	// Relationship type strings must never be altered — they are not node IDs.
	// This test explicitly verifies that a relationship type that happens to
	// start with a tenant prefix substring is left untouched.
	prefix := "0001@"
	input := map[string]string{
		"0001@post:1": "0001@weird_relationship", // bizarre but possible in theory
	}
	got := stripPrefixFromEdgeMap(prefix, input)
	// The key must be stripped; the value must not be.
	if _, ok := got["post:1"]; !ok {
		t.Error("key was not stripped")
	}
	if got["post:1"] != "0001@weird_relationship" {
		t.Errorf("relationship value was incorrectly stripped: got %q", got["post:1"])
	}
}

// ---------------------------------------------------------------------------
// stripPrefixFromSlice
// ---------------------------------------------------------------------------

func TestStripPrefixFromSlice(t *testing.T) {
	prefix := "0001@"
	input := []string{"0001@post:1", "0001@post:2", "0001@author:1"}
	got := stripPrefixFromSlice(prefix, input)

	want := []string{"post:1", "post:2", "author:1"}
	if len(got) != len(want) {
		t.Fatalf("length: want %d, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d]: want %q, got %q", i, want[i], got[i])
		}
	}
}

func TestStripPrefixFromSlice_EmptyPrefix(t *testing.T) {
	input := []string{"post:1", "post:2"}
	got := stripPrefixFromSlice("", input)
	if len(got) != 2 || got[0] != "post:1" || got[1] != "post:2" {
		t.Errorf("empty prefix: expected unchanged slice, got %v", got)
	}
}

func TestStripPrefixFromSlice_NilAndEmpty(t *testing.T) {
	if got := stripPrefixFromSlice("0001@", nil); got != nil {
		t.Errorf("nil slice: expected nil back, got %v", got)
	}
	empty := []string{}
	if got := stripPrefixFromSlice("0001@", empty); len(got) != 0 {
		t.Errorf("empty slice: expected empty back, got %v", got)
	}
}

func TestStripPrefixFromSlice_MixedPrefixes(t *testing.T) {
	// If a slice somehow contains a mix of prefixed and already-clean IDs
	// (which would indicate an upstream bug), the function must not corrupt
	// any entry: entries without the prefix are returned unchanged.
	prefix := "0001@"
	input := []string{"0001@post:1", "post:2"} // second has no prefix
	got := stripPrefixFromSlice(prefix, input)
	if got[0] != "post:1" {
		t.Errorf("[0]: want %q, got %q", "post:1", got[0])
	}
	if got[1] != "post:2" {
		t.Errorf("[1]: want %q, got %q", "post:2", got[1])
	}
}
