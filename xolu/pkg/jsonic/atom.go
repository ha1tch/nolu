// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package jsonic

import (
	"fmt"
	"sync"
	"unsafe"
)

// Atom is a uint64-packed representation of a field name for O(1) key
// matching during JSON token walking. Names up to 8 bytes are packed
// directly (one byte per position); longer names use FNV-1a hashing.
type Atom uint64

// MakeAtom converts a string to an Atom. For names <= 8 bytes, the
// packing is bijective (no collisions possible). For names > 8 bytes,
// FNV-1a is used and the caller should use AtomRegistry to detect
// hash collisions.
func MakeAtom(s string) Atom {
	if len(s) <= 8 {
		var a uint64
		for i := 0; i < len(s); i++ {
			a |= uint64(s[i]) << (i * 8)
		}
		return Atom(a)
	}
	return Atom(fnv1a(s))
}

// MakeAtomBytes converts a byte slice to an Atom without allocation.
func MakeAtomBytes(b []byte) Atom {
	if len(b) <= 8 {
		var a uint64
		for i := 0; i < len(b); i++ {
			a |= uint64(b[i]) << (i * 8)
		}
		return Atom(a)
	}
	return Atom(fnv1aBytes(b))
}

// MatchBytes reports whether a byte slice matches this Atom. For short
// names (<= 8 bytes), this is a single integer comparison. For hashed
// names, it compares the hash value.
func (a Atom) MatchBytes(b []byte) bool {
	return MakeAtomBytes(b) == a
}

// fnv1a computes the FNV-1a hash of a string.
func fnv1a(s string) uint64 {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

// fnv1aBytes computes the FNV-1a hash of a byte slice.
func fnv1aBytes(b []byte) uint64 {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(b); i++ {
		h ^= uint64(b[i])
		h *= 1099511628211
	}
	return h
}

// ---------------------------------------------------------------------------
// AtomRegistry: collision-safe name -> atom mapping
// ---------------------------------------------------------------------------

// AtomRegistry manages a set of field names and their Atom representations.
// It detects hash collisions for names longer than 8 bytes (where FNV-1a
// is used). For names <= 8 bytes, packing is bijective and collision-free.
//
// The registry is safe for concurrent reads after initial registration.
// Registration itself is mutex-protected.
type AtomRegistry struct {
	mu      sync.RWMutex
	byName  map[string]Atom
	byAtom  map[Atom]string // reverse lookup for collision detection
	needsFull map[Atom]bool // atoms that require full string verification
}

// NewAtomRegistry creates a new empty registry.
func NewAtomRegistry() *AtomRegistry {
	return &AtomRegistry{
		byName:    make(map[string]Atom),
		byAtom:    make(map[Atom]string),
		needsFull: make(map[Atom]bool),
	}
}

// Register adds a field name to the registry and returns its Atom.
// If the name collides with an existing (different) name, Register
// returns an error. This should be called at query planning time,
// not in the hot path.
func (r *AtomRegistry) Register(name string) (Atom, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if a, ok := r.byName[name]; ok {
		return a, nil // already registered
	}

	a := MakeAtom(name)

	// Check for collision: different name, same atom
	if existing, ok := r.byAtom[a]; ok && existing != name {
		return 0, fmt.Errorf("jsonic: atom collision between %q and %q (hash %016x)", name, existing, uint64(a))
	}

	r.byName[name] = a
	r.byAtom[a] = name

	// Names > 8 bytes use FNV-1a; mark them for full verification
	if len(name) > 8 {
		r.needsFull[a] = true
	}

	return a, nil
}

// MustRegister is like Register but panics on collision.
func (r *AtomRegistry) MustRegister(name string) Atom {
	a, err := r.Register(name)
	if err != nil {
		panic(err)
	}
	return a
}

// Lookup returns the Atom for a previously registered name.
// Returns (0, false) if the name was not registered.
func (r *AtomRegistry) Lookup(name string) (Atom, bool) {
	r.mu.RLock()
	a, ok := r.byName[name]
	r.mu.RUnlock()
	return a, ok
}

// NeedsFullVerify reports whether an atom match requires full string
// comparison (true for FNV-1a hashed names > 8 bytes).
func (r *AtomRegistry) NeedsFullVerify(a Atom) bool {
	r.mu.RLock()
	v := r.needsFull[a]
	r.mu.RUnlock()
	return v
}

// FullName returns the registered name for an atom.
// Returns ("", false) if the atom was not registered.
func (r *AtomRegistry) FullName(a Atom) (string, bool) {
	r.mu.RLock()
	name, ok := r.byAtom[a]
	r.mu.RUnlock()
	return name, ok
}

// VerifyMatch checks whether a byte slice from the JSON input truly
// matches an atom. For short names (<= 8 bytes), atom comparison is
// sufficient. For hashed names, it compares the full string.
func (r *AtomRegistry) VerifyMatch(a Atom, keyBytes []byte) bool {
	if !r.NeedsFullVerify(a) {
		return true // short name, atom comparison is exact
	}
	name, ok := r.FullName(a)
	if !ok {
		return false
	}
	return unsafeString(keyBytes) == name
}

// unsafeString converts a byte slice to a string without copying.
// The string is only valid as long as the underlying byte slice is alive.
func unsafeString(b []byte) string {
	return *(*string)(unsafe.Pointer(&b))
}
