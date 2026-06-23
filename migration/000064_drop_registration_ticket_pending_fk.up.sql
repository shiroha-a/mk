-- #2083: registration_ticket.pendingUserId の余分な FK を撤去する。
-- 000026 で `"pendingUserId" varchar(32) REFERENCES "user"("id") ON DELETE SET NULL`
-- と user(id) への FK を張っていたが、pendingUserId には **user_pending 行の ID**
-- (= user テーブルには存在しない) が入るため、確認メール再送防止 (MarkPending) で
-- 必ず FK 違反になり機能が production で no-op になっていた。upstream Misskey の
-- registration_ticket.pendingUserId は無制約の varchar (refactor-invite-system
-- 1688720440658 / RegistrationTicket.ts の bare @Column) なので、それに揃えて FK を
-- 落とす。usedById / createdById の FK は upstream にもあるので残す。
ALTER TABLE "registration_ticket" DROP CONSTRAINT IF EXISTS "registration_ticket_pendingUserId_fkey";
