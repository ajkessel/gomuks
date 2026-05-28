// gomuks - A Matrix client written in Go.
// Copyright (C) 2025 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
import { use, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react"
import { ScaleLoader } from "react-spinners"
import { EventID, MemDBEvent } from "@/api/types"
import ClientContext from "../ClientContext.ts"
import MainScreenContext from "../MainScreenContext.ts"
import { RoomContext, RoomContextData } from "../roomview/roomcontext.ts"
import TimelineEvent from "../timeline/TimelineEvent.tsx"

const BATCH_SIZE = 50

function stripDiacritics(str: string): string {
	return str.normalize("NFD").replace(/[̀-ͯ]/g, "").toLowerCase()
}

// Parse raw FTS query into plain terms for highlighting (strip FTS4 operators and search filters).
function queryToTerms(query: string): string[] {
	return query
		.replace(/\bfrom:(?:"[^"]*"|\S+)/gi, "")
		.replace(/\b(?:date|received):(?:"[^"]*"|\S+)/gi, "")
		.replace(/["*^]/g, " ")
		.trim().split(/\s+/).map(stripDiacritics).filter(Boolean)
}

function applyHighlights(el: HTMLElement, terms: string[]): void {
	if (!terms.length) {return}
	// Remove any highlights from a previous pass before re-applying.
	el.querySelectorAll("mark.search-highlight").forEach(mark => {
		mark.replaceWith(document.createTextNode(mark.textContent ?? ""))
	})
	el.normalize()

	const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT)
	const textNodes: Text[] = []
	let node: Node | null
	while ((node = walker.nextNode())) {
		if ((node as Text).parentElement?.closest("mark")) {continue}
		textNodes.push(node as Text)
	}

	for (const textNode of textNodes) {
		const original = textNode.nodeValue ?? ""
		if (!original.trim()) {continue}
		const normalized = stripDiacritics(original)

		const ranges: [number, number][] = []
		for (const term of terms) {
			let pos = 0
			while (pos < normalized.length) {
				const idx = normalized.indexOf(term, pos)
				if (idx === -1) {break}
				ranges.push([idx, idx + term.length])
				pos = idx + term.length
			}
		}
		if (!ranges.length) {continue}

		ranges.sort((a, b) => a[0] - b[0])
		const merged: [number, number][] = []
		for (const r of ranges) {
			const last = merged[merged.length - 1]
			if (last && r[0] <= last[1]) {
				last[1] = Math.max(last[1], r[1])
			} else {
				merged.push([r[0], r[1]])
			}
		}

		const fragment = document.createDocumentFragment()
		let pos = 0
		for (const [start, end] of merged) {
			if (pos < start) {fragment.appendChild(document.createTextNode(original.slice(pos, start)))}
			const mark = document.createElement("mark")
			mark.className = "search-highlight"
			mark.textContent = original.slice(start, end)
			fragment.appendChild(mark)
			pos = end
		}
		if (pos < original.length) {fragment.appendChild(document.createTextNode(original.slice(pos)))}
		textNode.parentNode?.replaceChild(fragment, textNode)
	}
}

interface SearchResultItemProps {
	evt: MemDBEvent
	prevEvt: MemDBEvent | null
	query: string
	roomName?: string
}

const SearchResultItem = ({ evt, prevEvt, query, roomName }: SearchResultItemProps) => {
	const activeRoomCtx = use(RoomContext)
	const client = use(ClientContext)!
	const mainScreen = use(MainScreenContext)
	const containerRef = useRef<HTMLDivElement>(null)
	const renderEvt = useMemo(() => {
		const newEvt = evt.redacted_by ? { ...evt, viewing_redacted: true } : { ...evt }
		const body = newEvt.content?.body
		if (typeof body === "string" && body.length > 250) {
			newEvt.content = { ...newEvt.content, body: body.slice(0, 250) + "…" }
			if (newEvt.local_content?.sanitized_html) {
				newEvt.local_content = { ...newEvt.local_content, sanitized_html: undefined }
			}
		}
		return newEvt
	}, [evt])
	const resultRoom = client.store.rooms.get(evt.room_id)
	const resultRoomCtx = useMemo(
		() => resultRoom ? new RoomContextData(resultRoom) : activeRoomCtx,
		[resultRoom, activeRoomCtx],
	)
	if (resultRoomCtx && resultRoom) {
		const setReplyInRoom = (eventID: EventID, thread: boolean) => {
			if (window.activeRoomContext?.store === resultRoom) {
				if (thread) {
					window.activeRoomContext.setReplyToAsThread(eventID)
				} else {
					window.activeRoomContext.setReplyTo(eventID)
				}
			} else {
				if (thread) {
					resultRoom.hackyPendingReplyToThreadEventID = eventID
				} else {
					resultRoom.hackyPendingReplyToEventID = eventID
				}
				mainScreen.setActiveRoom(resultRoom.roomID)
			}
		}
		resultRoomCtx.setReplyTo = eventID => {
			if (eventID) {
				setReplyInRoom(eventID, false)
			}
		}
		resultRoomCtx.setReplyToAsThread = eventID => setReplyInRoom(eventID, true)
		resultRoomCtx.jumpToEvent = eventID => {
			mainScreen.setActiveRoom(resultRoom.roomID, { openEventID: eventID })
		}
	}
	// Run after every render so re-renders of TimelineEvent (member load, decrypt)
	// get re-highlighted automatically.
	useLayoutEffect(() => {
		if (containerRef.current) {
			applyHighlights(containerRef.current, queryToTerms(query))
		}
	})
	return <>
		{roomName && <div className="search-result-room" title={roomName}>{roomName}</div>}
		<div className="search-result" ref={containerRef}>
			{evt.redacted_by && <div className="search-result-redacted">Deleted message</div>}
			<RoomContext value={resultRoomCtx}>
				<TimelineEvent evt={renderEvt} prevEvt={prevEvt} viewType="notifications" />
			</RoomContext>
		</div>
	</>
}

interface SearchPanelProps {
	initialQuery?: string
	initialRoomScoped?: boolean
}

const SearchPanel = ({ initialQuery = "", initialRoomScoped = true }: SearchPanelProps) => {
	const roomCtx = use(RoomContext)
	const client = use(ClientContext)!
	const [query, setQuery] = useState(initialQuery)
	const [submittedQuery, setSubmittedQuery] = useState(initialQuery)
	const [events, setEvents] = useState<MemDBEvent[]>([])
	const [loading, setLoading] = useState(false)
	const [error, setError] = useState<string | null>(null)
	const [hasMore, setHasMore] = useState(false)
	const [roomScoped, setRoomScoped] = useState(initialRoomScoped)
	const [includeDirect, setIncludeDirect] = useState(false)
	const [includeEncrypted, setIncludeEncrypted] = useState(false)
	const [includeNonMessages, setIncludeNonMessages] = useState(false)
	const [resultRoomScoped, setResultRoomScoped] = useState(initialRoomScoped)
	const [removeRedacted, setRemoveRedacted] = useState(false)
	const viewRef = useRef<HTMLDivElement>(null)
	const inputRef = useRef<HTMLInputElement>(null)

	useEffect(() => {
		inputRef.current?.focus()
	}, [])

	const setRoomScopeAndRefresh = (scoped: boolean) => {
		if (loading) {return}
		setRoomScoped(scoped)
		if (submittedQuery.trim()) {
			setEvents([])
			setHasMore(false)
			runSearch(submittedQuery, scoped, includeDirect, includeEncrypted, includeNonMessages, 0, [])
		}
	}

	useEffect(() => {
		const onKeyDown = (evt: KeyboardEvent) => {
			if (evt.ctrlKey && evt.altKey && !evt.shiftKey && evt.key.toLowerCase() === "r") {
				evt.preventDefault()
				evt.stopPropagation()
				setRoomScopeAndRefresh(!roomScoped)
			}
		}
		document.addEventListener("keydown", onKeyDown)
		return () => document.removeEventListener("keydown", onKeyDown)
	// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [roomScoped, includeDirect, includeEncrypted])

	useEffect(() => {
		if (!initialQuery) {
			return
		}
		runSearch(initialQuery, initialRoomScoped, includeDirect, includeEncrypted, includeNonMessages, 0, [])
	// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [])

	const runSearch = (
		q: string, scoped: boolean, incDirect: boolean, incEncrypted: boolean,
		incNonMessages: boolean, offset: number, existing: MemDBEvent[],
	) => {
		if (!q.trim()) {
			return
		}
		setLoading(true)
		setError(null)
		if (offset === 0) {
			setResultRoomScoped(scoped)
		}
		client.searchMessages({
			query: q,
			roomID: scoped ? roomCtx?.store.roomID : undefined,
			includeDirect: incDirect,
			includeEncrypted: incEncrypted,
			includeNonMessages: incNonMessages,
			limit: BATCH_SIZE,
			offset,
		}).then(
			res => {
				setEvents(existing.concat(res))
				setHasMore(res.length >= BATCH_SIZE)
			},
			err => setError(`${err}`),
		).finally(() => setLoading(false))
	}

	const handleSubmit = (e?: React.FormEvent) => {
		e?.preventDefault()
		setEvents([])
		setHasMore(false)
		setSubmittedQuery(query)
		runSearch(query, roomScoped, includeDirect, includeEncrypted, includeNonMessages, 0, [])
	}

	const loadMore = () => {
		runSearch(
			submittedQuery, roomScoped, includeDirect,
			includeEncrypted, includeNonMessages, events.length, events,
		)
	}

	const getRoomName = (evt: MemDBEvent) =>
		client.store.rooms.get(evt.room_id)?.meta.current.name
		|| client.store.roomListEntries.get(evt.room_id)?.name
		|| evt.room_id

	const hasRedactedResults = events.some(evt => evt.redacted_by)
	const visibleEvents = removeRedacted ? events.filter(evt => !evt.redacted_by) : events
	const hasResults = visibleEvents.length > 0
	const hasSearched = submittedQuery !== "" && !loading

	return <>
		<form className="controls" onSubmit={handleSubmit}>
			<input
				ref={inputRef}
				type="search"
				placeholder="Search messages..."
				value={query}
				onChange={e => setQuery(e.target.value)}
				className="search-input"
			/>
			<button type="submit" disabled={loading || !query.trim()}>Search</button>
			<label title="Toggle current room only (Ctrl+Alt+R)">
				Current room only
				<input
					type="checkbox"
					checked={roomScoped}
					onChange={e => setRoomScopeAndRefresh(e.target.checked)}
				/>
			</label>
			{!roomScoped && <>
				<label title="Include direct messages in results">
					Include PMs
					<input
						type="checkbox"
						checked={includeDirect}
						onChange={e => {
							setIncludeDirect(e.target.checked)
							if (submittedQuery.trim()) {
								setEvents([])
								setHasMore(false)
								runSearch(
									submittedQuery, roomScoped, e.target.checked,
									includeEncrypted, includeNonMessages, 0, [],
								)
							}
						}}
					/>
				</label>
				<label title="Include encrypted chats in results">
					Include E2EE
					<input
						type="checkbox"
						checked={includeEncrypted}
						onChange={e => {
							setIncludeEncrypted(e.target.checked)
							if (submittedQuery.trim()) {
								setEvents([])
								setHasMore(false)
								runSearch(
									submittedQuery, roomScoped, includeDirect,
									e.target.checked, includeNonMessages, 0, [],
								)
							}
						}}
					/>
				</label>
			</>}
			<label title="Include non-message events like joins/leaves in results">
				Include non-messages
				<input
					type="checkbox"
					checked={includeNonMessages}
					onChange={e => {
						setIncludeNonMessages(e.target.checked)
						if (submittedQuery.trim()) {
							setEvents([])
							setHasMore(false)
							runSearch(
								submittedQuery, roomScoped, includeDirect,
								includeEncrypted, e.target.checked, 0, [],
							)
						}
					}}
				/>
			</label>
			{hasRedactedResults && <label>
				Remove redacted
				<input
					type="checkbox"
					checked={removeRedacted}
					onChange={e => setRemoveRedacted(e.target.checked)}
				/>
			</label>}
			{error && <div className="error">{error}</div>}
		</form>
		<div className="search-panel-content" ref={viewRef}>
			{hasSearched && !hasResults && !loading && (
				<div className="empty-search">No results found for &ldquo;{submittedQuery}&rdquo;</div>
			)}
			{loading && !hasResults && (
				<div className="loading-search">
					<ScaleLoader color="var(--primary-color)"/> Searching...
				</div>
			)}
			{visibleEvents.map((evt, i) => {
				const prevEvt = visibleEvents[i-1] ?? null
				const showRoomName = !resultRoomScoped && prevEvt?.room_id !== evt.room_id
				return (
					<SearchResultItem
						key={evt.rowid}
						evt={evt}
						prevEvt={prevEvt}
						query={submittedQuery}
						roomName={showRoomName ? getRoomName(evt) : undefined}
					/>
				)
			})}
			{hasMore && (
				<button className="load-more" onClick={loadMore} disabled={loading}>
					{loading
						? <><ScaleLoader color="var(--primary-color)"/> Loading more results...</>
						: "Load more results"}
				</button>
			)}
		</div>
	</>
}

export default SearchPanel
