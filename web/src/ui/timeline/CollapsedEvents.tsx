// gomuks - A Matrix client written in Go.
// Copyright (C) 2026 Tulir Asokan
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
import React, { JSX, useState } from "react"
import { MemDBEvent } from "@/api/types"
import TimelineEvent, { TimelineEventViewType } from "./TimelineEvent.tsx"
import "./CollapsedEvents.css"

interface CollapsedEventsProps {
	events: MemDBEvent[]
	prevEvt: MemDBEvent | null
	smallReplies: boolean
	smallThreads: boolean
	focusedEventRowID: number | null
	viewType: TimelineEventViewType
}

const CollapsedEvents = ({
	events,
	prevEvt,
	smallReplies,
	smallThreads,
	focusedEventRowID,
	viewType,
}: CollapsedEventsProps) => {
	const [expanded, setExpanded] = useState(false)

	if (expanded) {
		const renderedEvents: JSX.Element[] = []
		let currentPrev = prevEvt
		for (const evt of events) {
			renderedEvents.push(<TimelineEvent
				key={evt.rowid}
				evt={evt}
				prevEvt={currentPrev}
				smallReplies={smallReplies}
				smallThreads={smallThreads}
				isFocused={focusedEventRowID === evt.rowid}
				viewType={viewType}
			/>)
			currentPrev = evt
		}
		return <>{renderedEvents}</>
	}

	const count = events.length
	return <div className="collapsed-events" onClick={() => setExpanded(true)}>
		<div className="collapsed-events-line" />
		<button className="collapsed-events-button">
			{count} system events
		</button>
		<div className="collapsed-events-line" />
	</div>
}

export default React.memo(CollapsedEvents)
