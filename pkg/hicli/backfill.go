// Copyright (c) 2024 Tulir Asokan
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package hicli

import (
	"context"
	"errors"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/id"
)

// RunBackfillQueue runs as a background goroutine after the initial sync completes,
// paginatingbackwards through all rooms to populate local history for search.
func (h *HiClient) RunBackfillQueue(ctx context.Context) {
	select {
	case <-h.backfillReady:
	case <-ctx.Done():
		return
	}

	log := zerolog.Ctx(ctx).With().Str("action", "backfill").Logger()
	log.Info().Int("history_days", *h.BackfillHistoryDays).Msg("Starting background history backfill")

	var cutoff time.Time
	if *h.BackfillHistoryDays > 0 {
		cutoff = time.Now().AddDate(0, 0, -*h.BackfillHistoryDays)
	}

	maxTS := time.Now()
	const roomBatch = 50
	for {
		rooms, err := h.DB.Room.GetBySortTS(ctx, maxTS, roomBatch)
		if err != nil {
			log.Err(err).Msg("Failed to get rooms for backfill")
			return
		}
		if len(rooms) == 0 {
			break
		}
		for _, room := range rooms {
			select {
			case <-ctx.Done():
				return
			default:
			}
			h.backfillRoom(ctx, room.ID, cutoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
		if len(rooms) < roomBatch {
			break
		}
		maxTS = rooms[len(rooms)-1].SortingTimestamp.Time
	}
	log.Info().Msg("Background history backfill complete")
}

func (h *HiClient) backfillRoom(ctx context.Context, roomID id.RoomID, cutoff time.Time) {
	log := zerolog.Ctx(ctx).With().Str("action", "backfill").Stringer("room_id", roomID).Logger()
	log.Debug().Msg("Starting room history backfill")
	pages := 0
	for {
		resp, err := h.PaginateServer(ctx, roomID, 100, false)
		if errors.Is(err, ErrPaginationAlreadyInProgress) {
			log.Debug().Msg("Skipping room backfill: pagination already in progress")
			return
		} else if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Err(err).Msg("Failed to backfill room history")
			return
		}
		pages++
		log.Debug().Int("page", pages).Int("events", len(resp.Events)).Bool("has_more", resp.HasMore).Msg("Fetched backfill page")
		if !resp.HasMore {
			log.Debug().Int("pages", pages).Msg("Room history fully backfilled")
			return
		}
		if !cutoff.IsZero() && len(resp.Events) > 0 {
			oldest := resp.Events[len(resp.Events)-1]
			if oldest.Timestamp.Time.Before(cutoff) {
				log.Debug().Int("pages", pages).Time("oldest_event", oldest.Timestamp.Time).Msg("Reached backfill cutoff date")
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}
