// chat spec 共通の型 (#822, #823)。messages_dm / room /
// messaging_notification の各 spec で chat/messages/* / chat/rooms/*
// response の最小 shape を共有する。

// ChatMessage は chat/messages/* response の最小 shape。upstream / mk-go で
// 共通する field のみ含む。fileId / reactions など spec 固有の追加 field を
// 検証する場合は spec 側で extends すれば良い。
//
// shape drift (#851): upstream TS は `null` の field を JSON response から
// omit する (= undefined)、mk-go は明示的に `null` を返す。本 interface は
// `null` 表現で揃えてあるが、両表現を吸収するために spec 側で
// `expect(field ?? null).toBeNull()` の pattern を使う。
export interface ChatMessage {
  id: string;
  fromUserId: string;
  toUserId: string | null;
  toRoomId: string | null;
  text: string | null;
  createdAt: string;
}

// ChatRoom は chat/rooms/* response の shape。upstream は false / null の
// field を omit する drift (#851) があるため、boolean / nullable field は
// optional で受け取り、spec 側で `?? false` / `?? null` で吸収する。
export interface ChatRoom {
  id: string;
  name: string;
  ownerId: string;
  description: string | null;
  isArchived: boolean;
}
