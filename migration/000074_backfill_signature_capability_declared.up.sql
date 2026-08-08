-- instance_signature_capability の宣言 (ed25519DeclaredAt) を既存データから初期化する。
--
-- 宣言は本来 actor resolve のたびに記録されるが、それだけだと「既に連合済みだが
-- 次の actor 再解決までまだ間がある」ホストが長期間 null のままになる。稼働中の
-- インスタンスでは大半の連合先がこれに当たるため、機能を入れた直後は表示がほぼ空
-- になってしまう。user_publickey_extra には resolve 済み actor の Ed25519 鍵が既に
-- 溜まっているので、そこから host 単位に畳んで初期値を入れる。
--
-- 記録する時刻は now()。「この鍵は過去のいつ確認されたか」は残っていないが、
-- resolver は actor JSON から消えた keyId を purge するので、テーブルに残っている
-- 鍵は最後の resolve 時点で有効だったものに限られる。したがって now() は
-- 「移行時点で保存済みの鍵から確認した」という意味で正しい。
--
-- 000073 とは別ファイルにしている。000073 は既に適用済みの環境があり、そこを
-- 編集しても再実行されないため。schema 変更と data backfill を分けておくと、
-- backfill だけを後から差し替えることもできる。
INSERT INTO "instance_signature_capability" ("host", "ed25519DeclaredAt", "updatedAt")
SELECT u.host, now(), now()
FROM "user_publickey_extra" e
JOIN "user" u ON u.id = e."userId"
WHERE u.host IS NOT NULL
  AND u.host <> ''
  AND e.alg = 'ed25519'
GROUP BY u.host
-- 既に観測済みの host は触らない。観測 (受信 / 配送成功) の方が宣言より強い
-- シグナルなので、backfill が上書きしてはいけない。
ON CONFLICT ("host") DO NOTHING;
