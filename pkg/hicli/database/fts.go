// Copyright (c) 2025 Tulir Asokan
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

//go:build cgo

package database

import (
	"go.mau.fi/util/dbutil/litestream"
)

func init() {
	litestream.Functions["normalize_fts"] = normalizeFTS
}
