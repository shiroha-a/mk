-- Misskey 本家の AbuseReportNotificationRecipient entity は updatedAt
-- (timestamp with time zone, default CURRENT_TIMESTAMP) を持つ。mk-go の
-- 000028 はこの列を欠いており、admin/abuse-report/notification-recipient の
-- 応答が golden 必須の updatedAt を欠落していた (#1294 で検出)。drop-in DB
-- 互換のため列を追加する。既存行には now() を埋める。
ALTER TABLE "abuse_report_notification_recipient"
    ADD COLUMN IF NOT EXISTS "updatedAt" timestamp with time zone NOT NULL DEFAULT now();
