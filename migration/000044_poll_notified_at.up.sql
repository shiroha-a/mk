-- #690: poll table に notifiedAt を追加。expiresAt 経過後の pollEnded 通知
-- が author + 投票者に発火済みかを記録し、periodic ticker (core/poll/
-- expiry_worker) が二重通知を避ける。
-- nullable timestamp。NULL は「まだ送ってない」、NOT NULL は「送信済み (時刻)」。
ALTER TABLE "poll" ADD COLUMN "notifiedAt" timestamp with time zone;

-- expiresAt < NOW() AND notifiedAt IS NULL を高速 scan するための部分 index。
-- ticker が頻繁に空 scan するため、expired AND unnotified のみを対象にする
-- partial index にすることで size を最小化する。
CREATE INDEX IF NOT EXISTS "IDX_poll_expired_unnotified"
  ON "poll" ("expiresAt")
  WHERE "expiresAt" IS NOT NULL AND "notifiedAt" IS NULL;
