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
import { use } from "react"
import { getAvatarThumbnailURL, getAvatarURL } from "@/api/media.ts"
import { RoomStateStore, useMultipleRoomMembers, useReadReceipts } from "@/api/statestore"
import { EventID } from "@/api/types"
import { getDisplayname } from "@/util/validation.ts"
import ClientContext from "../ClientContext.ts"
import { LightboxContext, ModalCloseContext } from "./contexts.ts"
import CloseIcon from "@/icons/close.svg?react"
import "./ReadReceiptsModal.css"

interface ReadReceiptsModalProps {
	room: RoomStateStore
	eventID: EventID
	extraEvents?: EventID[]
}

const fullTimeFormatter = new Intl.DateTimeFormat("en-GB", { dateStyle: "full", timeStyle: "medium" })

const ReadReceiptsModal = ({ room, eventID, extraEvents }: ReadReceiptsModalProps) => {
	const client = use(ClientContext)!
	const closeModal = use(ModalCloseContext)
	const receipts = useReadReceipts(room, eventID, extraEvents)
	const memberEvts = useMultipleRoomMembers(client, room, receipts.map(receipt => receipt.user_id))

	const receiptList = receipts.map((receipt, i) => {
		const [userID, member] = memberEvts[i] || [receipt.user_id, null]
		const timestamp = new Date(receipt.timestamp)
		return <div key={userID} className="read-receipt-item" title={fullTimeFormatter.format(timestamp)}>
			<img
				className="avatar"
				loading="lazy"
				src={getAvatarThumbnailURL(userID, member)}
				data-full-src={getAvatarURL(userID, member)}
				onClick={use(LightboxContext)}
				alt=""
			/>
			<div className="member-info">
				<div className="displayname">{getDisplayname(userID, member)}</div>
				<div className="userid">{userID}</div>
			</div>
			<div className="timestamp">{timestamp.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}</div>
		</div>
	})

	return <div className="read-receipts-modal">
		<div className="modal-header">
			<h3>Read by</h3>
			<button className="close-button" onClick={closeModal} title="Close">
				<CloseIcon />
			</button>
		</div>
		<div className="receipt-list">
			{receiptList}
		</div>
	</div>
}

export default ReadReceiptsModal
