// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package jsonic

import (
	"fmt"
	"unsafe"
)

// TokenType identifies the kind of JSON token.
type TokenType uint8

const (
	TokString   TokenType = iota // Quoted string (Start/End exclude quotes)
	TokNumber                    // Numeric literal
	TokTrue                      // true
	TokFalse                     // false
	TokNull                      // null
	TokObjStart                  // {
	TokObjEnd                    // }
	TokArrStart                  // [
	TokArrEnd                    // ]
	TokColon                     // :
	TokComma                     // ,
)

// Token represents a single JSON token as a (type, start, end) triple
// referencing byte offsets in the original input. No copies are made.
type Token struct {
	Type  TokenType
	Start uint32 // byte offset of token content start
	End   uint32 // byte offset past last byte of token content
}

// Tokeniser is a zero-allocation JSON tokeniser that produces a flat
// array of tokens referencing the original input byte slice.
type Tokeniser struct {
	input  []byte
	pos    int
	tokens []Token
}

// poolSize is the capacity of the tokeniser pool channel.
const poolSize = 16

var tokenPool = make(chan *Tokeniser, poolSize)

// GetTokeniser returns a tokeniser from the pool, or allocates a new one.
func GetTokeniser() *Tokeniser {
	select {
	case t := <-tokenPool:
		t.tokens = t.tokens[:0]
		return t
	default:
		return &Tokeniser{tokens: make([]Token, 0, 256)}
	}
}

// PutTokeniser returns a tokeniser to the pool for reuse. The input
// reference is cleared to avoid retaining large byte slices.
func PutTokeniser(t *Tokeniser) {
	t.input = nil
	t.pos = 0
	// Prevent unbounded token slice growth
	if cap(t.tokens) > 4096 {
		t.tokens = make([]Token, 0, 256)
	}
	select {
	case tokenPool <- t:
	default:
	}
}

// Tokenise parses the input bytes into tokens. The input must be valid
// JSON. The tokeniser references the input slice directly; the caller
// must keep the input alive while tokens are in use.
func (t *Tokeniser) Tokenise(input []byte) error {
	t.input = input
	t.pos = 0
	t.tokens = t.tokens[:0]
	return t.parseValue()
}

// Tokens returns the token slice produced by the last Tokenise call.
func (t *Tokeniser) Tokens() []Token { return t.tokens }

// Input returns the input byte slice from the last Tokenise call.
func (t *Tokeniser) Input() []byte { return t.input }

// TokenCount returns the number of tokens produced.
func (t *Tokeniser) TokenCount() int { return len(t.tokens) }

// TokenString extracts the string content of a TokString token from the
// input. This is a zero-copy operation using unsafeString; the returned
// string is only valid while the input byte slice is alive.
func (t *Tokeniser) TokenString(tok Token) string {
	return unsafeString(t.input[tok.Start:tok.End])
}

// TokenBytes returns the raw bytes of a token from the input.
func (t *Tokeniser) TokenBytes(tok Token) []byte {
	return t.input[tok.Start:tok.End]
}

// ---------------------------------------------------------------------------
// Whitespace skipping
// ---------------------------------------------------------------------------

// skipWS advances past whitespace bytes one at a time.
func (t *Tokeniser) skipWS() {
	for t.pos < len(t.input) {
		switch t.input[t.pos] {
		case ' ', '\t', '\n', '\r':
			t.pos++
		default:
			return
		}
	}
}

// skipWS8 uses SWAR (SIMD Within A Register) to process 8 bytes at a
// time for whitespace skipping. Falls back to byte-by-byte for the
// trailing bytes and when non-whitespace is found within a chunk.
func (t *Tokeniser) skipWS8() {
	for t.pos+8 <= len(t.input) {
		chunk := *(*uint64)(unsafe.Pointer(&t.input[t.pos]))
		// XOR with each whitespace character, check for zero bytes
		sp := chunk ^ 0x2020202020202020 // space
		tb := chunk ^ 0x0909090909090909 // tab
		lf := chunk ^ 0x0A0A0A0A0A0A0A0A // newline
		cr := chunk ^ 0x0D0D0D0D0D0D0D0D // carriage return

		hasZero := func(v uint64) bool {
			return (v-0x0101010101010101)&^v&0x8080808080808080 != 0
		}
		if !hasZero(sp) && !hasZero(tb) && !hasZero(lf) && !hasZero(cr) {
			return // no whitespace in this 8-byte chunk
		}
		// Fallback: byte-by-byte within the chunk
		for i := 0; i < 8 && t.pos < len(t.input); i++ {
			switch t.input[t.pos] {
			case ' ', '\t', '\n', '\r':
				t.pos++
			default:
				return
			}
		}
	}
	t.skipWS()
}

// ---------------------------------------------------------------------------
// Token emission
// ---------------------------------------------------------------------------

func (t *Tokeniser) add(typ TokenType, start, end int) {
	t.tokens = append(t.tokens, Token{Type: typ, Start: uint32(start), End: uint32(end)})
}

// ---------------------------------------------------------------------------
// Recursive descent parser
// ---------------------------------------------------------------------------

func (t *Tokeniser) parseValue() error {
	t.skipWS8()
	if t.pos >= len(t.input) {
		return fmt.Errorf("jsonic: unexpected end of input")
	}
	switch t.input[t.pos] {
	case '{':
		return t.parseObject()
	case '[':
		return t.parseArray()
	case '"':
		return t.parseString()
	case 't':
		return t.parseLiteral("true", TokTrue)
	case 'f':
		return t.parseLiteral("false", TokFalse)
	case 'n':
		return t.parseLiteral("null", TokNull)
	default:
		if t.input[t.pos] == '-' || (t.input[t.pos] >= '0' && t.input[t.pos] <= '9') {
			return t.parseNumber()
		}
		return fmt.Errorf("jsonic: unexpected character %c at position %d", t.input[t.pos], t.pos)
	}
}

func (t *Tokeniser) parseObject() error {
	t.add(TokObjStart, t.pos, t.pos+1)
	t.pos++
	first := true
	for {
		t.skipWS8()
		if t.pos >= len(t.input) {
			return fmt.Errorf("jsonic: unterminated object")
		}
		if t.input[t.pos] == '}' {
			t.add(TokObjEnd, t.pos, t.pos+1)
			t.pos++
			return nil
		}
		if !first {
			if t.input[t.pos] != ',' {
				return fmt.Errorf("jsonic: expected comma at position %d", t.pos)
			}
			t.add(TokComma, t.pos, t.pos+1)
			t.pos++
			t.skipWS8()
		}
		first = false
		if err := t.parseString(); err != nil {
			return err
		}
		t.skipWS8()
		if t.pos >= len(t.input) || t.input[t.pos] != ':' {
			return fmt.Errorf("jsonic: expected colon at position %d", t.pos)
		}
		t.add(TokColon, t.pos, t.pos+1)
		t.pos++
		if err := t.parseValue(); err != nil {
			return err
		}
	}
}

func (t *Tokeniser) parseArray() error {
	t.add(TokArrStart, t.pos, t.pos+1)
	t.pos++
	first := true
	for {
		t.skipWS8()
		if t.pos >= len(t.input) {
			return fmt.Errorf("jsonic: unterminated array")
		}
		if t.input[t.pos] == ']' {
			t.add(TokArrEnd, t.pos, t.pos+1)
			t.pos++
			return nil
		}
		if !first {
			if t.input[t.pos] != ',' {
				return fmt.Errorf("jsonic: expected comma at position %d", t.pos)
			}
			t.add(TokComma, t.pos, t.pos+1)
			t.pos++
		}
		first = false
		if err := t.parseValue(); err != nil {
			return err
		}
	}
}

func (t *Tokeniser) parseString() error {
	if t.pos >= len(t.input) || t.input[t.pos] != '"' {
		return fmt.Errorf("jsonic: expected quote at position %d", t.pos)
	}
	start := t.pos + 1
	t.pos++
	for t.pos < len(t.input) {
		if t.input[t.pos] == '\\' {
			t.pos += 2
			continue
		}
		if t.input[t.pos] == '"' {
			t.add(TokString, start, t.pos)
			t.pos++
			return nil
		}
		t.pos++
	}
	return fmt.Errorf("jsonic: unterminated string starting at position %d", start-1)
}

func (t *Tokeniser) parseNumber() error {
	start := t.pos
	if t.input[t.pos] == '-' {
		t.pos++
	}
	for t.pos < len(t.input) && t.input[t.pos] >= '0' && t.input[t.pos] <= '9' {
		t.pos++
	}
	if t.pos < len(t.input) && t.input[t.pos] == '.' {
		t.pos++
		for t.pos < len(t.input) && t.input[t.pos] >= '0' && t.input[t.pos] <= '9' {
			t.pos++
		}
	}
	if t.pos < len(t.input) && (t.input[t.pos] == 'e' || t.input[t.pos] == 'E') {
		t.pos++
		if t.pos < len(t.input) && (t.input[t.pos] == '+' || t.input[t.pos] == '-') {
			t.pos++
		}
		for t.pos < len(t.input) && t.input[t.pos] >= '0' && t.input[t.pos] <= '9' {
			t.pos++
		}
	}
	t.add(TokNumber, start, t.pos)
	return nil
}

func (t *Tokeniser) parseLiteral(lit string, typ TokenType) error {
	if t.pos+len(lit) > len(t.input) {
		return fmt.Errorf("jsonic: unexpected end parsing %s", lit)
	}
	for i := 0; i < len(lit); i++ {
		if t.input[t.pos+i] != lit[i] {
			return fmt.Errorf("jsonic: expected %s at position %d", lit, t.pos)
		}
	}
	t.add(typ, t.pos, t.pos+len(lit))
	t.pos += len(lit)
	return nil
}

// ---------------------------------------------------------------------------
// Token navigation helpers
// ---------------------------------------------------------------------------

// SkipValue advances past a single JSON value (including nested
// objects and arrays) starting at token index i. Returns the index
// of the next token after the skipped value.
func SkipValue(tokens []Token, i int) int {
	if i >= len(tokens) {
		return i
	}
	switch tokens[i].Type {
	case TokObjStart:
		depth := 1
		i++
		for i < len(tokens) && depth > 0 {
			if tokens[i].Type == TokObjStart {
				depth++
			} else if tokens[i].Type == TokObjEnd {
				depth--
			}
			i++
		}
		return i
	case TokArrStart:
		depth := 1
		i++
		for i < len(tokens) && depth > 0 {
			if tokens[i].Type == TokArrStart {
				depth++
			} else if tokens[i].Type == TokArrEnd {
				depth--
			}
			i++
		}
		return i
	default:
		return i + 1
	}
}
