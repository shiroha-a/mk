-- access_token.token に index を追加 (#910 review nit)。
--
-- middleware は hash 列 OR token 列の dual lookup で resolve する
-- (FindByHashOrToken)。hash 列には初版から index があったが、token 列
-- (= raw token, app/auth flow で middleware hit する経路) は未 indexed
-- だったため、PostgreSQL は OR query で seq scan に fallback していた。
-- upstream Misskey TS の Init migration には token / hash 両方の index が
-- あるので、drop-in compat として揃える。
CREATE INDEX IF NOT EXISTS "IDX_access_token_token" ON "access_token" ("token");
