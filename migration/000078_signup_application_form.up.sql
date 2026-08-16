-- 申請フォームを管理者が定義できるようにする (#2568 / #2570)。
--
-- MiAuth をやめたことで、fediverse アカウントは「検証しない単なるテキスト欄」に
-- なった。固定の列として持つのではなく、管理者が項目を組める形にする。
--
-- 定義の形 (要素の配列):
--   [{"label": "参加の動機", "type": "textarea", "required": true, "maxLength": 1000}]
--
-- 回答の形 (提出時のラベルを添えた配列):
--   [{"label": "参加の動機", "value": "..."}]
--
-- **回答にラベルを同梱するのが要点。** 定義を後から変えると、既存申請の回答が
-- どの設問への答えか分からなくなる。提出時のラベルをスナップショットとして持て
-- ば、審査画面が後から壊れない。
--
-- ラベルは**サーバーが定義から埋める**。クライアントに送らせると、申請者が
-- 審査画面に偽のラベルを流し込める。
ALTER TABLE "meta" ADD COLUMN IF NOT EXISTS "signupApplicationForm" jsonb NOT NULL DEFAULT '[]';
ALTER TABLE "signup_application" ADD COLUMN IF NOT EXISTS "answers" jsonb NOT NULL DEFAULT '[]';

-- 固定の申請理由欄は上の仕組みに吸収される。
-- data loss: 既存申請の申請理由が失われる。承認制はまだ運用に出していない想定。
ALTER TABLE "signup_application" DROP COLUMN IF EXISTS "reason";
