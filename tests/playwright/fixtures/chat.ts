// chat spec 共通の型 (#822, #823)。messages_dm / room /
// messaging_notification の各 spec で chat/messages/* / chat/rooms/*
// response の最小 shape を共有する。

// ChatMessage は chat/messages/* response の最小 shape。upstream の
// `packedChatMessageSchema` (json-schema/chat-message.ts) と整合する形で、
// `toUserId` / `toRoomId` / `text` / `fileId` は optional として定義する。
// upstream / mk-go ともに値が無い場合は JSON response から field を omit する
// (#851 fix 後)。
export interface ChatMessage {
  id: string;
  fromUserId: string;
  toUserId?: string;
  toRoomId?: string;
  text?: string;
  createdAt: string;
}

// ChatRoom は chat/rooms/* response の最小 shape。upstream の
// `packedChatRoomSchema` に揃え、`isArchived` は含めない (#851 fix 後)。
// archived state は別経路で取得する設計。
export interface ChatRoom {
  id: string;
  name: string;
  ownerId: string;
  description: string;
}
