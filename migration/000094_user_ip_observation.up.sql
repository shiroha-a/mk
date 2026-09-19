-- #3103: IP 履歴を関連アカウント検索 (#3066) の材料にできる形へ整える。
--
-- 1. `createdAt` の意味を upstream に揃える (= 初回観測)。
--    upstream の `ApiCallService.logIp` は `INSERT ... orIgnore` なので、衝突時に
--    `createdAt` を更新しない。mk-go は `Upsert` で `createdAt` を上書きしており、
--    **同じ列が mk-go では最終観測、upstream では初回観測**という乖離になっていた。
--    純正から引き継いだ DB では 1 つの列に両方の意味の行が混ざる。
--    ここでは列の意味を upstream 側へ倒し、最終観測は独自列で持つ。
--
-- 2. 最終観測 (`lastSeenAt`) と観測回数 (`observationCount`) を独自列として足す。
--    #3066 は「対象IPの初回観測日時・最終観測日時」と「観測回数」を要求するが、
--    `user_ip` は `UNIQUE (userId, ip)` で 1 ペア 1 行しか持たないため、列を足さずに
--    出す方法が無い (`signin` を数える案は `enableIpLogging` を迂回するので採らない)。
--
-- 3. IP の表記を正規化する。IPv4-mapped IPv6 を対応する IPv4 へ畳む。
--
-- 4. 「その IP を使ったアカウント」を引く index を足す。既存の index は
--    `userId` と `UNIQUE (userId, ip)` だけで、**IP 単独の検索は seq scan** になる。

-- **DEFAULT が要る。** 純正 Misskey はこの 2 列を知らないので、drop-in で TS 側へ
-- 戻した構成が `(createdAt, userId, ip)` だけを INSERT する。DEFAULT が無いと
-- NOT NULL 違反で書き込みが落ちる。
ALTER TABLE "user_ip" ADD COLUMN IF NOT EXISTS "lastSeenAt" timestamp with time zone;
ALTER TABLE "user_ip" ADD COLUMN IF NOT EXISTS "observationCount" integer NOT NULL DEFAULT 1;

-- 既存行の最終観測は分からない。**mk-go が書いた行の `createdAt` は最終観測、
-- 純正が書いた行は初回観測**だが、どちらも「その時刻に観測した」ことは確かなので
-- 最終観測として写す。mk-go 由来の行では初回 = 最終になり、初回は復元できない。
UPDATE "user_ip" SET "lastSeenAt" = "createdAt" WHERE "lastSeenAt" IS NULL;
ALTER TABLE "user_ip" ALTER COLUMN "lastSeenAt" SET NOT NULL;
ALTER TABLE "user_ip" ALTER COLUMN "lastSeenAt" SET DEFAULT now();

-- 既存行の IP を正規化する。
--
-- **畳むと `(userId, ip)` が衝突しうるので、統合してから書き換える。** 統合は
-- 初回 = 古い方、最終 = 新しい方、回数 = 合算。
--
-- **PL/pgSQL で 1 文にまとめてある。** `ip` は varchar なので `::inet` が失敗しうるが
-- PostgreSQL に try-cast が無く、正規表現の guard では取りこぼす。例外を握って
-- その行だけ飛ばす形が確実。一時オブジェクトを作らないのは、migration の各文が
-- 同じ接続で走る保証が無いため。
--
-- **`inet` が読めない形は畳めない。** `misc/ipnorm` が Go 側で受ける形のうち、
-- `1.2.3.4:5678` (port 付き) と `fe80::1%eth0` (zone 付き) はここでは正規化されず
-- 元のまま残る。**`::192.0.2.1` (IPv4-compatible、非推奨) も揃わない** — PostgreSQL は
-- その表記のまま描画し、Go の `netip` は `::c000:201` にする。これらの行は新しい
-- 観測とは別の行として残り、#3066 の完全一致検索からは漏れる (保持期間で消える)。
-- 記録側は #3103 以降すべて正規形を書くので、増えることはない。
--
-- **CIDR は触らない。** `192.0.2.0/24` は `inet` として読めてしまい、`host()` が
-- `192.0.2.0` を返す = **実在しない観測を作る**。IP として保存されるはずのない値
-- なので、`/` を含む行は正規化の対象から外す。
DO $$
DECLARE
    r RECORD;
    norm text;
    keeper bigint;
BEGIN
    FOR r IN
        SELECT id, "userId", ip, "createdAt", "lastSeenAt", "observationCount"
        -- **`ORDER BY id` も保険。** PostgreSQL は小さいテーブルを物理順で返し、
        -- それが id 順と一致するので外しても結果は変わらない (実測で変異が検出
        -- されない)。UPDATE / VACUUM を挟んだ実 DB では順序が変わり、**どの行が
        -- 生き残るかが非決定**になる。生き残る id は `admin/get-user-ips` の並びに出る。
        FROM "user_ip" ORDER BY id
    LOOP
        CONTINUE WHEN position('/' IN r.ip) > 0;
        BEGIN
            -- **前後の空白は先に落とす。** `inet` は空白付きを受け付けない
            -- (`'  192.0.2.1  '::inet` は syntax error) ので、btrim が無いと
            -- 例外で CONTINUE に落ち、その行は正規化されないまま残る。
            -- `btrim` が落とすのは半角空白だけなので、タブ付きは Go 側
            -- (`strings.TrimSpace`) と揃わない。
            norm := host(btrim(r.ip)::inet);
        EXCEPTION WHEN others THEN
            -- IP として読めない値は触らない (消しもしない)。
            CONTINUE;
        END;
        -- PostgreSQL は IPv4-mapped を `::ffff:a.b.c.d` で描画する。`inet` 自体は
        -- 畳まないので、ここで接頭辞を落とす。
        IF norm LIKE '::ffff:%.%' THEN
            norm := substring(norm from 8);
        END IF;
        -- 既に正規形なら触らない。**外しても結果は同じだが、全行に no-op の
        -- UPDATE が走る** (大きいインスタンスでは migration 時間と WAL 量に出る)。
        CONTINUE WHEN norm = r.ip;

        -- **`u.id <> r.id` は保険。** ここへ来る時点で `norm <> r.ip` が確定して
        -- いる (直前の `CONTINUE WHEN`) ので、`u.ip = norm` が自分自身に一致する
        -- ことはない。外しても振る舞いは同じ (実測で変異が検出されない)。
        SELECT u.id INTO keeper FROM "user_ip" u
        WHERE u."userId" = r."userId" AND u.ip = norm AND u.id <> r.id
        LIMIT 1;

        IF keeper IS NULL THEN
            UPDATE "user_ip" SET ip = norm WHERE id = r.id;
        ELSE
            UPDATE "user_ip" SET
                "createdAt" = LEAST("createdAt", r."createdAt"),
                "lastSeenAt" = GREATEST("lastSeenAt", r."lastSeenAt"),
                "observationCount" = "observationCount" + r."observationCount"
            WHERE id = keeper;
            DELETE FROM "user_ip" WHERE id = r.id;
        END IF;
    END LOOP;
END $$;

-- 「その IP を使ったアカウントを最終観測の新しい順に」が #3066 の中核クエリ。
CREATE INDEX IF NOT EXISTS "IDX_user_ip_ip_lastSeenAt" ON "user_ip" ("ip", "lastSeenAt" DESC);
