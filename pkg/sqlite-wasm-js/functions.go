// Copyright (c) 2026 Tulir Asokan
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

//go:build js

package sqlite_wasm_js

import (
	"strings"
	"sync/atomic"
	"syscall/js"
	"unicode"

	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

var indexRedacted atomic.Bool

func init() {
	indexRedacted.Store(true)
}

func SetIndexRedacted(enabled bool) {
	indexRedacted.Store(enabled)
}

func normalizeFTS(text string) string {
	t := transform.Chain(norm.NFD, transform.RemoveFunc(func(r rune) bool {
		return unicode.Is(unicode.Mn, r)
	}))
	result, _, _ := transform.String(t, strings.ToLower(text))
	return result
}

func (c *Conn) registerFunctions() (err error) {
	registered := make([]js.Func, 0, 1)
	defer func() {
		if err != nil {
			for _, fn := range registered {
				fn.Release()
			}
		}
	}()
	defer catchIntoError(&err)

	normalize := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 2 || args[1].IsNull() || args[1].IsUndefined() {
			return nil
		}
		return normalizeFTS(args[1].String())
	})
	registered = append(registered, normalize)
	indexRedactedFn := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		return indexRedacted.Load()
	})
	registered = append(registered, indexRedactedFn)
	c.ptr.Call("createFunction", map[string]any{
		"name":          "normalize_fts",
		"xFunc":         normalize,
		"arity":         1,
		"deterministic": true,
		"innocuous":     true,
	})
	c.ptr.Call("createFunction", map[string]any{
		"name":          "index_redacted",
		"xFunc":         indexRedactedFn,
		"arity":         0,
		"deterministic": false,
		"innocuous":     true,
	})
	c.udfs = append(c.udfs, registered...)
	return nil
}
