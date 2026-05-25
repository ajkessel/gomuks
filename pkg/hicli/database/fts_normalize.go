// Copyright (c) 2025 Tulir Asokan
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package database

import (
	"strings"
	"unicode"

	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// normalizeFTS strips diacritical marks and lowercases text for FTS indexing.
// Combined with the porter tokenizer this gives both accent-folding and stemming.
func normalizeFTS(text string) string {
	t := transform.Chain(norm.NFD, transform.RemoveFunc(func(r rune) bool {
		return unicode.Is(unicode.Mn, r)
	}))
	result, _, _ := transform.String(t, strings.ToLower(text))
	return result
}

// normalizeFTSQuery strips diacritical marks from an FTS query without lowercasing.
// Preserving case is required so that FTS4 boolean operators (AND, OR, NOT, NEAR)
// remain uppercase and are interpreted as operators rather than literal search terms.
// The porter tokenizer handles case-folding for individual search terms.
func normalizeFTSQuery(query string) string {
	t := transform.Chain(norm.NFD, transform.RemoveFunc(func(r rune) bool {
		return unicode.Is(unicode.Mn, r)
	}))
	result, _, _ := transform.String(t, query)
	return result
}
