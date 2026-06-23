-- FK を復元する (000026 と同じ user(id) 参照 / ON DELETE SET NULL)。down 適用後は
-- MarkPending が再び FK 違反になる点に注意 (元の bug 状態に戻る)。
ALTER TABLE "registration_ticket"
    ADD CONSTRAINT "registration_ticket_pendingUserId_fkey"
    FOREIGN KEY ("pendingUserId") REFERENCES "user"("id") ON DELETE SET NULL;
