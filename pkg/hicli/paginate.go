// Copyright (c) 2024 Tulir Asokan
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package hicli

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/gomuks/pkg/hicli/database"
	"go.mau.fi/gomuks/pkg/hicli/jsoncmd"
)

var ErrPaginationAlreadyInProgress = errors.New("pagination is already in progress")

func (h *HiClient) GetEvent(ctx context.Context, roomID id.RoomID, eventID id.EventID) (*database.Event, error) {
	if evt, err := h.DB.Event.GetByID(ctx, roomID, eventID); err != nil {
		return nil, fmt.Errorf("failed to get event from database: %w", err)
	} else if evt != nil {
		h.ReprocessExistingEvent(ctx, evt)
		return evt, nil
	} else if serverEvt, err := h.Client.GetEvent(ctx, roomID, eventID); err != nil {
		return nil, fmt.Errorf("failed to get event from server: %w", err)
	} else {
		return h.processEvent(ctx, serverEvt, nil, nil, false)
	}
}

func (h *HiClient) GetUnredactedEvent(ctx context.Context, roomID id.RoomID, eventID id.EventID) (*database.Event, error) {
	if evt, err := h.DB.Event.GetByID(ctx, roomID, eventID); err != nil {
		return nil, fmt.Errorf("failed to get event from database: %w", err)
		// TODO this check doesn't handle events which keep some fields on redaction
	} else if evt != nil && len(evt.Content) > 2 {
		h.ReprocessExistingEvent(ctx, evt)
		return evt, nil
	} else if serverEvt, err := h.Client.GetUnredactedEventContent(ctx, roomID, eventID); err != nil {
		return nil, fmt.Errorf("failed to get event from server: %w", err)
	} else if redactedServerEvt, err := h.Client.GetEvent(ctx, roomID, eventID); err != nil {
		return nil, fmt.Errorf("failed to get redacted event from server: %w", err)
		// TODO this check will have false positives on actually empty events
	} else if len(serverEvt.Content.VeryRaw) == 2 {
		return nil, fmt.Errorf("server didn't return content")
	} else {
		serverEvt.Unsigned.RedactedBecause = redactedServerEvt.Unsigned.RedactedBecause
		return h.processEvent(ctx, serverEvt, nil, nil, false)
	}
}

func (h *HiClient) processStateReset(ctx context.Context, roomID id.RoomID, err error) bool {
	if !errors.Is(err, mautrix.MForbidden) {
		return false
	}
	log := zerolog.Ctx(ctx)
	joinedRooms, err := h.Client.JoinedRooms(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to fetch joined rooms to check if join event was reset")
		return false
	}
	if slices.Contains(joinedRooms.JoinedRooms, roomID) {
		log.Debug().Msg("Fetching state failed, but room is still in joined rooms")
		return false
	}
	log.Info().Msg("Fetching room state failed and room is not in joined rooms, deleting from database")
	err = h.DB.Room.Delete(ctx, roomID)
	if err != nil {
		log.Err(err).Msg("Failed to delete room from database after state reset")
	}
	h.EventHandler(&jsoncmd.SyncComplete{
		LeftRooms: []id.RoomID{roomID},
	})
	return true
}

func (h *HiClient) processGetRoomState(ctx context.Context, roomID id.RoomID, fetchMembers, refetch, dispatchEvt bool) error {
	var evts []*event.Event
	if refetch {
		resp, err := h.Client.StateAsArray(ctx, roomID)
		if err != nil {
			go h.processStateReset(context.WithoutCancel(ctx), roomID, err)
			return fmt.Errorf("failed to refetch state: %w", err)
		}
		evts = resp
	} else if fetchMembers {
		resp, err := h.Client.Members(ctx, roomID)
		if err != nil {
			go h.processStateReset(context.WithoutCancel(ctx), roomID, err)
			return fmt.Errorf("failed to fetch members: %w", err)
		}
		evts = resp.Chunk
	}
	if evts == nil {
		return nil
	}
	dbEvts := make([]*database.Event, len(evts))
	currentStateEntries := make([]*database.CurrentStateEntry, len(evts))
	mediaReferenceEntries := make([]*database.MediaReference, len(evts))
	mediaCacheEntries := make([]*database.PlainMedia, 0, len(evts))
	var joinedMembers, invitedMembers int
	var joinedOrInvitedMemberIDs, leftMemberIDs []id.UserID
	var hasSelf bool
	for i, evt := range evts {
		if err := h.fillPrevContent(ctx, evt); err != nil {
			return err
		}
		dbEvts[i] = database.MautrixToEvent(evt)
		currentStateEntries[i] = &database.CurrentStateEntry{
			EventType: evt.Type,
			StateKey:  *evt.StateKey,
		}
		var mediaURL string
		if evt.Type == event.StateMember {
			membership := event.Membership(evt.Content.Raw["membership"].(string))
			userID := id.UserID(*evt.StateKey)
			if userID != h.Account.UserID {
				if membership == event.MembershipJoin {
					joinedOrInvitedMemberIDs = append(joinedOrInvitedMemberIDs, userID)
					joinedMembers++
				} else if membership == event.MembershipInvite {
					invitedMembers++
					joinedOrInvitedMemberIDs = append(joinedOrInvitedMemberIDs, userID)
				} else {
					leftMemberIDs = append(leftMemberIDs, userID)
				}
			} else if membership == event.MembershipJoin {
				hasSelf = true
				joinedMembers++
			}
			currentStateEntries[i].Membership = membership
			mediaURL, _ = evt.Content.Raw["avatar_url"].(string)
		} else if evt.Type == event.StateRoomAvatar {
			mediaURL, _ = evt.Content.Raw["url"].(string)
		}
		if mxc := id.ContentURIString(mediaURL).ParseOrIgnore(); mxc.IsValid() {
			mediaCacheEntries = append(mediaCacheEntries, (*database.PlainMedia)(&mxc))
			mediaReferenceEntries[i] = &database.MediaReference{
				MediaMXC: mxc,
			}
		}
	}
	// World-readable rooms may allow fetching state even if the user has left,
	// so make sure our own member event is present.
	if !hasSelf {
		if h.processStateReset(context.WithoutCancel(ctx), roomID, mautrix.MForbidden) {
			return nil
		}
		zerolog.Ctx(ctx).Warn().Msg("Own member event not found in state, but listing rooms didn't delete it")
	}
	llSummary := &mautrix.LazyLoadSummary{
		JoinedMemberCount:  &joinedMembers,
		InvitedMemberCount: &invitedMembers,
	}
	if len(joinedOrInvitedMemberIDs) > 0 {
		llSummary.Heroes = joinedOrInvitedMemberIDs
	} else {
		llSummary.Heroes = leftMemberIDs
	}
	fullHeroes := llSummary.Heroes
	if len(llSummary.Heroes) > 5 {
		llSummary.Heroes = llSummary.Heroes[:5]
	}
	return h.DB.DoTxn(ctx, nil, func(ctx context.Context) error {
		room, err := h.DB.Room.Get(ctx, roomID)
		if err != nil {
			return fmt.Errorf("failed to get room from database: %w", err)
		} else if room == nil {
			return fmt.Errorf("room not found")
		}
		updatedRoom := &database.Room{
			ID:            room.ID,
			HasMemberList: true,
			NameQuality:   room.NameQuality,
		}
		if room.LazyLoadSummary != nil && room.LazyLoadSummary.Heroes != nil {
			allFound := true
			for _, hero := range room.LazyLoadSummary.Heroes {
				if !slices.Contains(fullHeroes, hero) {
					allFound = false
					break
				}
			}
			if allFound {
				// Preserve original heroes if they are all still present
				llSummary.Heroes = room.LazyLoadSummary.Heroes
			}
		}
		err = h.DB.Event.MassUpsertState(ctx, dbEvts)
		if err != nil {
			return fmt.Errorf("failed to save events: %w", err)
		}
		sdc := &spaceDataCollector{}
		for i := range currentStateEntries {
			currentStateEntries[i].EventRowID = dbEvts[i].RowID
			if mediaReferenceEntries[i] != nil {
				mediaReferenceEntries[i].EventRowID = dbEvts[i].RowID
			}
			if evts[i].Type != event.StateMember {
				processImportantEvent(ctx, evts[i], room, updatedRoom, dbEvts[i].RowID, sdc)
			}
		}
		err = h.DB.Media.AddMany(ctx, mediaCacheEntries)
		if err != nil {
			return fmt.Errorf("failed to save media cache entries: %w", err)
		}
		mediaReferenceEntries = slices.DeleteFunc(mediaReferenceEntries, func(reference *database.MediaReference) bool {
			return reference == nil
		})
		err = h.DB.Media.AddManyReferences(ctx, mediaReferenceEntries)
		if err != nil {
			return fmt.Errorf("failed to save media reference entries: %w", err)
		}
		err = h.DB.CurrentState.AddMany(ctx, room.ID, refetch, currentStateEntries)
		if err != nil {
			return fmt.Errorf("failed to save current state entries: %w", err)
		}
		if updatedRoom.NameQuality <= database.NameQualityParticipants {
			dmRoomName, dmAvatarURL, err := h.calculateRoomParticipantName(ctx, room.ID, llSummary)
			if err != nil {
				return fmt.Errorf("failed to calculate room name: %w", err)
			}
			updatedRoom.Name = &dmRoomName
			updatedRoom.NameQuality = database.NameQualityParticipants
			if !room.ExplicitAvatar && ptr.Val(updatedRoom.Avatar) != dmAvatarURL {
				updatedRoom.Avatar = &dmAvatarURL
			}
		}
		roomChanged := updatedRoom.CheckChangesAndCopyInto(room)
		// TODO dispatch space edge changes if something changed? (fairly unlikely though)
		err = sdc.Apply(ctx, room, h.DB.SpaceEdge)
		if err != nil {
			return err
		}
		if roomChanged {
			// Only set this here so it doesn't unconditionally flag the room as changed
			updatedRoom.LazyLoadSummary = llSummary
			err = h.DB.Room.Upsert(ctx, updatedRoom)
			if err != nil {
				return fmt.Errorf("failed to save room data: %w", err)
			}
			if dispatchEvt {
				h.EventHandler(&jsoncmd.SyncComplete{
					Rooms: map[id.RoomID]*jsoncmd.SyncRoom{
						roomID: {
							Meta: room,
						},
					},
				})
			}
		}
		return nil
	})
}

func (h *HiClient) GetRoomState(ctx context.Context, roomID id.RoomID, includeMembers, fetchMembers, refetch bool) ([]*database.Event, error) {
	if fetchMembers || refetch {
		if !includeMembers {
			go func(ctx context.Context) {
				err := h.processGetRoomState(ctx, roomID, fetchMembers, refetch, true)
				if err != nil {
					zerolog.Ctx(ctx).Err(err).Msg("Failed to fetch room state in background")
				}
			}(context.WithoutCancel(ctx))
		} else {
			err := h.processGetRoomState(ctx, roomID, fetchMembers, refetch, true)
			if err != nil {
				return nil, err
			}
		}
	}
	if !includeMembers {
		return h.DB.CurrentState.GetAllExceptMembers(ctx, roomID)
	}
	return h.DB.CurrentState.GetAll(ctx, roomID)
}

func (h *HiClient) Paginate(ctx context.Context, roomID id.RoomID, maxTimelineID database.TimelineRowID, limit int, reset bool) (*jsoncmd.PaginationResponse, error) {
	var evts []*database.Event
	var err error
	if reset {
		err = h.DB.Timeline.Clear(ctx, roomID)
		if err != nil {
			return nil, fmt.Errorf("failed to clear timeline: %w", err)
		}
	} else {
		evts, err = h.DB.Timeline.Get(ctx, roomID, limit, maxTimelineID)
		if err != nil {
			return nil, err
		}
	}
	var resp *jsoncmd.PaginationResponse
	if len(evts) > 0 {
		for _, evt := range evts {
			h.ReprocessExistingEvent(ctx, evt)
		}
		resp = &jsoncmd.PaginationResponse{Events: evts, HasMore: true}
	} else {
		resp, err = h.PaginateServer(ctx, roomID, limit, reset)
		if err != nil {
			return nil, err
		}
	}
	resp.RelatedEvents = make([]*database.Event, 0)
	eventIDs := make([]id.EventID, len(resp.Events))
	eventMap := make(map[id.EventID]struct{})
	for i := len(resp.Events) - 1; i >= 0; i-- {
		evt := resp.Events[i]
		eventIDs[i] = evt.ID
		eventMap[evt.ID] = struct{}{}
		replyTo := evt.GetReplyTo()
		if replyTo != "" {
			_, replyToAdded := eventMap[replyTo]
			if !replyToAdded {
				dbEvt, err := h.DB.Event.GetByID(ctx, roomID, replyTo)
				if err != nil {
					return nil, fmt.Errorf("failed to get reply-to event: %w", err)
				} else if dbEvt != nil {
					resp.RelatedEvents = append(resp.RelatedEvents, dbEvt)
					eventMap[replyTo] = struct{}{}
				}
			}
		}
	}
	resp.Receipts, err = h.GetReceipts(ctx, roomID, eventIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to get receipts: %w", err)
	}
	return resp, nil
}

func (h *HiClient) GetReceipts(ctx context.Context, roomID id.RoomID, eventIDs []id.EventID) (map[id.EventID][]*database.Receipt, error) {
	receipts, err := h.DB.Receipt.GetManyRead(ctx, roomID, eventIDs)
	if err != nil {
		return nil, err
	}
	encounteredUsers := map[id.UserID]struct{}{
		// Never include own receipts
		h.Account.UserID: {},
	}
	// If there are multiple receipts (e.g. due to threads), only keep the one for the latest event (first in the array)
	// The input event IDs are already sorted in reverse chronological order
	for _, evtID := range eventIDs {
		receiptArr := receipts[evtID]
		i := 0
		for _, receipt := range receiptArr {
			_, alreadyEncountered := encounteredUsers[receipt.UserID]
			if alreadyEncountered {
				continue
			}
			// Clear room ID for efficiency
			receipt.RoomID = ""
			encounteredUsers[receipt.UserID] = struct{}{}
			receiptArr[i] = receipt
			i++
		}
		if len(receiptArr) > 0 && i < len(receiptArr) {
			receipts[evtID] = receiptArr[:i]
		}
	}
	return receipts, nil
}

func (h *HiClient) PaginateServer(ctx context.Context, roomID id.RoomID, limit int, reset bool) (*jsoncmd.PaginationResponse, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(context.Canceled)
	h.paginationInterrupterLock.Lock()
	if _, alreadyPaginating := h.paginationInterrupter[roomID]; alreadyPaginating {
		h.paginationInterrupterLock.Unlock()
		return nil, ErrPaginationAlreadyInProgress
	}
	h.paginationInterrupter[roomID] = cancel
	h.paginationInterrupterLock.Unlock()
	defer func() {
		h.paginationInterrupterLock.Lock()
		delete(h.paginationInterrupter, roomID)
		h.paginationInterrupterLock.Unlock()
	}()

	room, err := h.DB.Room.Get(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("failed to get room from database: %w", err)
	} else if room == nil {
		return nil, fmt.Errorf("not in room %s", roomID)
	}
	if reset {
		room.PrevBatch = ""
	}
	if room.PrevBatch == database.PrevBatchPaginationComplete {
		return &jsoncmd.PaginationResponse{Events: []*database.Event{}, HasMore: false}, nil
	}
	resp, err := h.Client.Messages(ctx, roomID, room.PrevBatch, "", mautrix.DirectionBackward, nil, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get messages from server: %w", err)
	}
	events := make([]*database.Event, len(resp.Chunk))
	if resp.End == "" {
		resp.End = database.PrevBatchPaginationComplete
	}
	if len(resp.Chunk) == 0 {
		err = h.DB.Room.SetPrevBatch(ctx, room.ID, resp.End)
		if err != nil {
			return nil, fmt.Errorf("failed to set prev_batch: %w", err)
		}
		return &jsoncmd.PaginationResponse{
			Events:     events,
			FromServer: true,
			HasMore:    resp.End != database.PrevBatchPaginationComplete,
		}, nil
	}
	wakeupSessionRequests := false
	decryptionQueue := make(map[id.SessionID]*database.SessionRequest)
	processedEvents := make([]*database.Event, len(resp.Chunk))
	for i, evt := range resp.Chunk {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		processedEvents[i], err = h.processEvent(ctx, evt, room.LazyLoadSummary, decryptionQueue, true)
		if err != nil {
			return nil, err
		}
	}
	err = h.DB.DoTxn(ctx, nil, func(ctx context.Context) error {
		if err = ctx.Err(); err != nil {
			return err
		}
		eventRowIDs := make([]database.EventRowID, len(resp.Chunk))
		iOffset := 0
		duplicateCount := 0
		for i, dbEvt := range processedEvents {
			if exists, err := h.DB.Timeline.Has(ctx, roomID, dbEvt.RowID); err != nil {
				return fmt.Errorf("failed to check if event exists in timeline: %w", err)
			} else if exists {
				duplicateCount++
				iOffset++
				continue
			}
			events[i-iOffset] = dbEvt
			eventRowIDs[i-iOffset] = events[i-iOffset].RowID
		}
		if duplicateCount > 0 {
			zerolog.Ctx(ctx).Debug().
				Int("duplicate_count", duplicateCount).
				Int("page_size", len(resp.Chunk)).
				Msg("Skipped events that already exist in timeline")
		}
		wakeupSessionRequests = len(decryptionQueue) > 0
		for _, entry := range decryptionQueue {
			err = h.DB.SessionRequest.Put(ctx, entry)
			if err != nil {
				return fmt.Errorf("failed to save session request for %s: %w", entry.SessionID, err)
			}
		}
		err = h.DB.Room.SetPrevBatch(ctx, room.ID, resp.End)
		if err != nil {
			return fmt.Errorf("failed to set prev_batch: %w", err)
		}
		if iOffset >= len(events) {
			events = events[:0]
			return nil
		}
		events = events[:len(events)-iOffset]
		eventRowIDs = eventRowIDs[:len(eventRowIDs)-iOffset]
		err = h.DB.Event.FillReactionCounts(ctx, roomID, events)
		if err != nil {
			return fmt.Errorf("failed to fill reaction counts: %w", err)
		}
		err = h.DB.Event.FillLastEditRowIDs(ctx, roomID, events)
		if err != nil {
			return fmt.Errorf("failed to fill last edit row IDs: %w", err)
		}
		var tuples []database.TimelineRowTuple
		tuples, err = h.DB.Timeline.Prepend(ctx, room.ID, eventRowIDs)
		if err != nil {
			return fmt.Errorf("failed to prepend events to timeline: %w", err)
		}
		for i, evt := range events {
			evt.TimelineRowID = tuples[i].Timeline
		}
		return nil
	})
	if err == nil && wakeupSessionRequests {
		h.WakeupRequestQueue()
	}
	return &jsoncmd.PaginationResponse{
		Events:     events,
		HasMore:    resp.End != database.PrevBatchPaginationComplete,
		FromServer: true,
	}, err
}

func (h *HiClient) GetEventContext(ctx context.Context, roomID id.RoomID, eventID id.EventID, limit int) (*jsoncmd.EventContextResponse, error) {
	if resp, err := h.getLocalEventContext(ctx, roomID, eventID, limit); err == nil {
		return resp, nil
	}
	filter := &mautrix.FilterPart{LazyLoadMembers: true}
	resp, err := h.Client.Context(ctx, roomID, eventID, filter, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get event context: %w", err)
	}
	wrappedResp := &jsoncmd.EventContextResponse{
		Start:  resp.Start,
		End:    resp.End,
		Before: make([]*database.Event, len(resp.EventsBefore)),
		After:  make([]*database.Event, len(resp.EventsAfter)),
	}
	decryptionQueue := make(map[id.SessionID]*database.SessionRequest)
	wrappedResp.Event, err = h.processEvent(ctx, resp.Event, nil, decryptionQueue, true)
	if err != nil {
		return nil, fmt.Errorf("failed to process event: %w", err)
	}
	for i, evt := range resp.EventsBefore {
		if wrappedResp.Before[i], err = h.processEvent(ctx, evt, nil, decryptionQueue, true); err != nil {
			return nil, fmt.Errorf("failed to process before event #%d: %w", i+1, err)
		}
	}
	for i, evt := range resp.EventsAfter {
		if wrappedResp.After[i], err = h.processEvent(ctx, evt, nil, decryptionQueue, true); err != nil {
			return nil, fmt.Errorf("failed to process after event #%d: %w", i+1, err)
		}
	}
	for _, entry := range decryptionQueue {
		err = h.DB.SessionRequest.Put(ctx, entry)
		if err != nil {
			return nil, fmt.Errorf("failed to save session request for %s: %w", entry.SessionID, err)
		}
	}
	if len(decryptionQueue) > 0 {
		h.WakeupRequestQueue()
	}
	return wrappedResp, nil
}

func (h *HiClient) getLocalEventContext(ctx context.Context, roomID id.RoomID, eventID id.EventID, limit int) (*jsoncmd.EventContextResponse, error) {
	evt, err := h.DB.Event.GetByID(ctx, roomID, eventID)
	if err != nil || evt == nil {
		return nil, fmt.Errorf("event not found locally")
	}
	before, after, err := h.DB.Timeline.GetContextByEventID(ctx, roomID, eventID, limit)
	if err != nil {
		if !errors.Is(err, database.ErrEventNotInTimeline) {
			return nil, err
		}
		before, after, err = h.DB.Event.GetContext(ctx, evt, limit)
		if err != nil {
			return nil, err
		}
	}
	h.ReprocessExistingEvent(ctx, evt)
	resp := &jsoncmd.EventContextResponse{
		Before: make([]*database.Event, len(before)),
		After:  make([]*database.Event, len(after)),
		Event:  evt,
	}
	for i, e := range before {
		h.ReprocessExistingEvent(ctx, e)
		resp.Before[i] = e
	}
	for i, e := range after {
		h.ReprocessExistingEvent(ctx, e)
		resp.After[i] = e
	}
	return resp, nil
}

func (h *HiClient) PaginateManual(
	ctx context.Context,
	roomID id.RoomID,
	threadRoot id.EventID,
	since string,
	direction mautrix.Direction,
	limit int,
) (*jsoncmd.ManualPaginationResponse, error) {
	var chunk []*event.Event
	var wrappedResp jsoncmd.ManualPaginationResponse
	if threadRoot == "" {
		resp, err := h.Client.Messages(ctx, roomID, since, "", direction, nil, limit)
		if err != nil {
			return nil, fmt.Errorf("failed to get messages from server: %w", err)
		}
		chunk = resp.Chunk
		wrappedResp.NextBatch = resp.End
	} else {
		resp, err := h.Client.GetRelations(ctx, roomID, threadRoot, &mautrix.ReqGetRelations{
			RelationType: event.RelThread,
			Dir:          direction,
			From:         since,
			Limit:        limit,
			Recurse:      true,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to get thread messages from server: %w", err)
		}
		chunk = resp.Chunk
		wrappedResp.NextBatch = resp.NextBatch
	}
	wrappedResp.Events = make([]*database.Event, len(chunk))
	decryptionQueue := make(map[id.SessionID]*database.SessionRequest)
	var err error
	for i, evt := range chunk {
		if wrappedResp.Events[i], err = h.processEvent(ctx, evt, nil, decryptionQueue, true); err != nil {
			return nil, fmt.Errorf("failed to process event #%d: %w", i+1, err)
		}
	}
	for _, entry := range decryptionQueue {
		err = h.DB.SessionRequest.Put(ctx, entry)
		if err != nil {
			return nil, fmt.Errorf("failed to save session request for %s: %w", entry.SessionID, err)
		}
	}
	if len(decryptionQueue) > 0 {
		h.WakeupRequestQueue()
	}
	return &wrappedResp, nil
}

func (h *HiClient) GetMentions(ctx context.Context, maxTS time.Time, unreadType database.UnreadType, limit int, roomID id.RoomID) ([]*database.Event, error) {
	evts, err := h.DB.Event.GetMentions(ctx, maxTS, unreadType, limit, roomID)
	for _, evt := range evts {
		h.ReprocessExistingEvent(ctx, evt)
	}
	return evts, err
}

var fromFilterRegex = regexp.MustCompile(`(?i)\bfrom:(?:"([^"]+)"|(\S+))`)
var dateFilterRegex = regexp.MustCompile(`(?i)\b(?:date|received):("(?:[^"]+)"|\d{1,2}/\d{1,2}/\d{2,4}-(?:\d{1,2}/\d{1,2}/\d{2,4})?|-\d{1,2}/\d{1,2}/\d{2,4}|[<>]?\d{1,2}/\d{1,2}/\d{2,4}|[a-z]+)`)

const (
	defaultSearchLimit = 50
	maxSearchLimit     = 100
)

var weekdayNames = map[string]time.Weekday{
	"sunday":    time.Sunday,
	"monday":    time.Monday,
	"tuesday":   time.Tuesday,
	"wednesday": time.Wednesday,
	"thursday":  time.Thursday,
	"friday":    time.Friday,
	"saturday":  time.Saturday,
}

func parseNaturalDate(phrase string) (start, end time.Time, ok bool) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	switch strings.ToLower(strings.TrimSpace(phrase)) {
	case "today":
		return today, today, true
	case "yesterday":
		y := today.AddDate(0, 0, -1)
		return y, y, true
	case "this week":
		wd := int(today.Weekday())
		if wd == 0 {
			wd = 7
		}
		monday := today.AddDate(0, 0, -(wd - 1))
		return monday, monday.AddDate(0, 0, 6), true
	case "last week":
		wd := int(today.Weekday())
		if wd == 0 {
			wd = 7
		}
		monday := today.AddDate(0, 0, -(wd-1)-7)
		return monday, monday.AddDate(0, 0, 6), true
	case "this month":
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
		last := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, time.Local)
		return first, last, true
	case "last month":
		first := time.Date(now.Year(), now.Month()-1, 1, 0, 0, 0, 0, time.Local)
		last := time.Date(now.Year(), now.Month(), 0, 0, 0, 0, 0, time.Local)
		return first, last, true
	case "this year":
		return time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.Local),
			time.Date(now.Year(), 12, 31, 0, 0, 0, 0, time.Local), true
	case "last year":
		return time.Date(now.Year()-1, 1, 1, 0, 0, 0, 0, time.Local),
			time.Date(now.Year()-1, 12, 31, 0, 0, 0, 0, time.Local), true
	default:
		lower := strings.ToLower(strings.TrimSpace(phrase))
		if after, found := strings.CutPrefix(lower, "last "); found {
			if wd, exists := weekdayNames[after]; exists {
				days := int(today.Weekday()) - int(wd)
				if days <= 0 {
					days += 7
				}
				t := today.AddDate(0, 0, -days)
				return t, t, true
			}
		}
	}
	return time.Time{}, time.Time{}, false
}

func parseDateValue(s string) (time.Time, error) {
	fields := strings.SplitN(s, "/", 3)
	if len(fields) != 3 {
		return time.Time{}, fmt.Errorf("invalid date %q", s)
	}
	month, err1 := strconv.Atoi(fields[0])
	day, err2 := strconv.Atoi(fields[1])
	year, err3 := strconv.Atoi(fields[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return time.Time{}, fmt.Errorf("invalid date %q", s)
	}
	if year < 100 {
		year += 2000
	}
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.Local), nil
}

func parseDateSpec(spec string) (startMs, endMs int64, err error) {
	var phrase string
	if strings.HasPrefix(spec, `"`) {
		phrase = strings.Trim(spec, `"`)
	} else if !strings.ContainsAny(spec, "0123456789") {
		phrase = spec
	}
	if phrase != "" {
		start, end, ok := parseNaturalDate(strings.TrimSpace(phrase))
		if !ok {
			err = fmt.Errorf("unrecognized date phrase %q", phrase)
			return
		}
		startMs = start.UnixMilli()
		endMs = time.Date(end.Year(), end.Month(), end.Day(), 23, 59, 59, 999000000, time.Local).UnixMilli()
		return
	}
	if after, ok := strings.CutPrefix(spec, ">"); ok {
		spec = after + "-"
	} else if after, ok := strings.CutPrefix(spec, "<"); ok {
		spec = "-" + after
	}
	parts := strings.SplitN(spec, "-", 2)
	var start, end time.Time
	if parts[0] != "" {
		if start, err = parseDateValue(parts[0]); err != nil {
			return
		}
	}
	if len(parts) == 2 {
		if parts[1] != "" {
			if end, err = parseDateValue(parts[1]); err != nil {
				return
			}
		}
	} else {
		end = start
	}
	if !start.IsZero() {
		startMs = start.UnixMilli()
	}
	if !end.IsZero() {
		endMs = time.Date(end.Year(), end.Month(), end.Day(), 23, 59, 59, 999000000, time.Local).UnixMilli()
	}
	return
}

func parseSearchQuery(raw string) (ftsQuery, senderName string, startTime, endTime int64, err error) {
	if m := fromFilterRegex.FindStringSubmatch(raw); m != nil {
		if m[1] != "" {
			senderName = strings.TrimSpace(m[1])
		} else {
			senderName = strings.TrimSpace(m[2])
		}
	}
	raw = fromFilterRegex.ReplaceAllString(raw, "")
	if m := dateFilterRegex.FindStringSubmatch(raw); m != nil {
		startTime, endTime, err = parseDateSpec(m[1])
		if err != nil {
			return
		}
	}
	raw = dateFilterRegex.ReplaceAllString(raw, "")
	ftsQuery = strings.TrimSpace(raw)
	return
}

func (h *HiClient) SearchMessages(ctx context.Context, query string, roomID id.RoomID, includeDirect, includeEncrypted, includeNonMessages bool, limit, offset int) ([]*database.Event, error) {
	if offset < 0 {
		return nil, fmt.Errorf("search offset must not be negative")
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	} else if limit > maxSearchLimit {
		limit = maxSearchLimit
	}
	ftsQuery, senderName, startTime, endTime, err := parseSearchQuery(query)
	if err != nil {
		return nil, err
	}
	h.Log.Info().
		Str("component", "sql").
		Str("query", query).
		Stringer("room_id", roomID).
		Bool("include_direct", includeDirect).
		Bool("include_encrypted", includeEncrypted).
		Bool("include_non_messages", includeNonMessages).
		Msg("Searching messages")
	evts, err := h.DB.Event.Search(ctx, ftsQuery, senderName, roomID, includeDirect, includeEncrypted, includeNonMessages, startTime, endTime, limit, offset)
	for _, evt := range evts {
		h.ReprocessExistingEvent(ctx, evt)
	}
	return evts, err
}
