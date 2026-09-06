-- #2858: owner が持ってしまった chat_room_membership / chat_room_invitation 行を
-- 後始末する。
--
-- upstream ChatService.ts は room のメンバー集合を
-- `memberships.concat({userId: room.ownerId, isMuted: false})` で作り、
-- 「ownerはmembershipレコードを作らないため」とコメントしている。owner は
-- membership 行を持たない、というのが不変条件。
--
-- `chat/rooms/transfer-ownership` は upstream Misskey に無い endpoint で
-- (出自は docs/divergence.md を見ること)、`ownerId` を書き換えるだけだったため、
-- 譲り受けた側の行が残っていた。
--
-- 残った行が起こすこと:
--   - `isMuted = true` のまま**解除できない設定**として残る。owner は
--     membership を見ずに `isMuted: false` を返す設計で、フロントも owner には
--     ミュートのスイッチを出さないため、利用者からは存在が見えない
--   - joined / joining 一覧に自分の owned room が出る (どちらも membership 経由)
--   - room-full の数え上げに owner が入り、上限が owner + 49 にずれる
--
-- **通知は届く。** fan-out は owner を mute 判定より前に never-muted として
-- seed する (`core/chat/service.go` の emitRoomNewChatMessage) ので、
-- この行があっても main stream / Web Push からは落ちない。
DELETE FROM "chat_room_membership" m
USING "chat_room" r
WHERE m."roomId" = r."id"
  AND m."userId" = r."ownerId";

-- **招待も同時に消す。** 譲渡は保留中の招待を消していなかったので、
-- 「owner 宛の招待」が残っている room がある。これを新 owner が accept すると
-- (`invitations/accept` / `rooms/join` / AP の Accept はいずれも owner か
-- どうかを見ていなかった)、上で消したばかりの membership 行が作り直される。
-- 片方だけ消しても意味が無い。
--
-- upstream にこの形の行は存在しない。`createRoomInvitation` は self-invite を
-- 弾き、`isRoomMember` が owner を true にするので既存メンバーとしても弾かれる。
DELETE FROM "chat_room_invitation" i
USING "chat_room" r
WHERE i."roomId" = r."id"
  AND i."userId" = r."ownerId";

-- **旧 owner 側は復旧できない。** 譲渡で room から締め出された利用者
-- (membership 行を持たないまま非 owner になった側) は、DB に「誰が旧 owner
-- だったか」の記録が無いため、この migration では戻せない。新しい
-- `transfer-ownership` は旧 owner に membership 行を作るので、今後は起きない。
-- 既に締め出された利用者は owner に再招待してもらう必要がある。
