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

const (
	backfillPageSize           = 100
	backfillContentionPause    = 5 * time.Second
	backfillMaxContentionPause = 30 * time.Second
	backfillInterPageDelay     = 200 * time.Millisecond
	backfillInterRoomDelay     = 500 * time.Millisecond
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
	if h.BackfillHistoryDays == nil {
		log.Error().Msg("RunBackfillQueue called with nil BackfillHistoryDays, skipping")
		return
	}
	log.Info().Int("history_days", *h.BackfillHistoryDays).Msg("Starting background history backfill")
	h.backfillActive.Store(true)
	defer h.backfillActive.Store(false)

	var cutoff time.Time
	if *h.BackfillHistoryDays >= 0 {
		cutoff = time.Now().AddDate(0, 0, -*h.BackfillHistoryDays)
	}

	maxTS := time.Now()
	var lastRoomID id.RoomID
	const roomBatch = 50
	for {
		rooms, err := h.DB.Room.GetBySortTS(ctx, maxTS, lastRoomID, roomBatch)
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
			if !h.waitForBackfillThrottle(ctx, log) {
				return
			}
			h.backfillRoom(ctx, room.ID, cutoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backfillInterRoomDelay):
			}
		}
		if len(rooms) < roomBatch {
			break
		}
		lastRoomID = rooms[len(rooms)-1].ID
		maxTS = rooms[len(rooms)-1].SortingTimestamp.Time
	}
	log.Info().Msg("Background history backfill complete")
}

func (h *HiClient) backfillRoom(ctx context.Context, roomID id.RoomID, cutoff time.Time) {
	log := zerolog.Ctx(ctx).With().
		Str("action", "backfill").
		Str("component", "sql").
		Stringer("room_id", roomID).
		Logger()
	log.Info().Msg("Starting room history backfill")
	pages := 0
	busyAttempts := 0
	for {
		if !h.waitForBackfillThrottle(ctx, log) {
			return
		}
		resp, err := h.PaginateServer(ctx, roomID, backfillPageSize, false)
		if errors.Is(err, ErrPaginationAlreadyInProgress) {
			log.Debug().Msg("Skipping room backfill: pagination already in progress")
			return
		} else if isDatabaseBusyError(err) {
			pause := h.slowBackfillAfterDatabaseBusy(busyAttempts)
			busyAttempts++
			log.Warn().Err(err).Dur("pause", pause).Msg("Database is busy during backfill, slowing down")
			continue
		} else if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Err(err).Msg("Failed to backfill room history")
			return
		}
		busyAttempts = 0
		pages++
		log.Debug().Int("page", pages).Int("events", len(resp.Events)).Bool("has_more", resp.HasMore).Msg("Fetched backfill page")
		if !resp.HasMore {
			log.Info().Int("pages", pages).Msg("Room history fully backfilled")
			return
		}
		if !cutoff.IsZero() && len(resp.Events) > 0 {
			oldest := resp.Events[len(resp.Events)-1]
			if oldest.Timestamp.Time.Before(cutoff) {
				log.Info().Int("pages", pages).Time("oldest_event", oldest.Timestamp.Time).Msg("Reached backfill cutoff date")
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backfillInterPageDelay):
		}
	}
}

func (h *HiClient) pauseBackfill(duration time.Duration) {
	until := time.Now().Add(duration).UnixMilli()
	for {
		current := h.backfillPauseUntil.Load()
		if current >= until || h.backfillPauseUntil.CompareAndSwap(current, until) {
			return
		}
	}
}

func (h *HiClient) slowBackfillAfterDatabaseBusy(attempt int) time.Duration {
	if !h.backfillActive.Load() {
		return 0
	}
	pause := min(time.Duration(attempt+1)*backfillContentionPause, backfillMaxContentionPause)
	h.pauseBackfill(pause)
	return pause
}

func (h *HiClient) waitForBackfillThrottle(ctx context.Context, log zerolog.Logger) bool {
	for {
		until := h.backfillPauseUntil.Load()
		delay := time.Until(time.UnixMilli(until))
		if delay <= 0 {
			return true
		}
		log.Debug().Dur("pause", delay).Msg("Pausing background history backfill after database contention")
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}
