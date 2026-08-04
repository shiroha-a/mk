-- リレー経由投稿の揮発化 (#2332)。Misskey TS に対応する機能は無いので mk-go
-- 独自の追加であり、upstream migration との対応も無い。TS へ切り戻しても未知の
-- 列は無視されるだけなので drop-in の復路は壊れない。
--
-- 本機能はテーブルを一切追加しない。リレー経由でしか観測しない投稿は Redis に
-- TTL 付きで置き、ローカルユーザーが触ったときだけ DB へ materialize する。
-- したがって追加するのは設定列のみ。

-- 既定を false にするのは、既存インスタンスの挙動を変えないため。有効化すると
-- グローバルタイムラインは FTT の窓より過去に遡れなくなる (窓から溢れた投稿は
-- Redis の TTL で消えるため) ので、管理者が意図的に入れる形にする。
ALTER TABLE "meta" ADD COLUMN IF NOT EXISTS "enableEphemeralRelayNotes" boolean NOT NULL DEFAULT false;

-- Redis 上の保持時間。FTT の窓より少し長く取れば足りる。窓から溢れた ID は
-- どのタイムラインからも参照されないため、それ以上持っても読まれない。
ALTER TABLE "meta" ADD COLUMN IF NOT EXISTS "ephemeralRelayNoteTtlMinutes" integer NOT NULL DEFAULT 60;
