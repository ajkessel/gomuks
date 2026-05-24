// Copyright (c) 2025 Tulir Asokan
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

//go:build cgo

package database

import (
	"strings"
	"unicode"

	"go.mau.fi/util/dbutil/litestream"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

func init() {
	litestream.Functions["normalize_fts"] = normalizeFTS
}

// normalizeFTS strips diacritical marks and lowercases text for FTS indexing.
// Combined with the porter tokenizer this gives both accent-folding and stemming.
func normalizeFTS(text string) string {
	t := transform.Chain(norm.NFD, transform.RemoveFunc(func(r rune) bool {
		return unicode.Is(unicode.Mn, r)
	}))
	result, _, _ := transform.String(t, strings.ToLower(text))
	return result
}
