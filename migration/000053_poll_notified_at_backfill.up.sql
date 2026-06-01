-- #1415: Misskey TS から mk-go へ移行した instance で、000044 が `poll.notifiedAt`
-- を NULL で追加した結果、既に expiresAt 経過後のアンケートが軒並み「未通知」
-- 状態として残り、ExpiryWorker の初回 tick で全 author + 投票者へ pollEnded
-- 通知が一斉発火する問題を、過去分を一括して通知済みにすることで止める。
--
-- 既に 000044 を適用済みかつ過去分の発火を既に踏んだ instance に対しては
-- no-op (notifiedAt が埋まっているため `notifiedAt IS NULL` で素通り)。
-- 000044 を含めて新規インストールする instance では (古い expired poll が
-- 存在しないため) UPDATE は 0 行で no-op。
--
-- expiresAt 値を notifiedAt にコピーするのは upstream Misskey TS が
-- 「通知時刻 = 期限到達時刻」として扱う semantics に合わせる意図 (= 後から
-- backfill された行も新規 expire と timestamp 上は区別がつかない)。
UPDATE "poll"
   SET "notifiedAt" = "expiresAt"
 WHERE "expiresAt" IS NOT NULL
   AND "expiresAt" < NOW()
   AND "notifiedAt" IS NULL;
