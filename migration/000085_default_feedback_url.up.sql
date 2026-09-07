-- meta.feedbackUrl が「未設定」のインスタンスで、frontend の /about に
-- フィードバックの導線を出し、nodeinfo の metadata にも値が載るようにする (#2891)。
--
-- **000084 (repositoryUrl) とまったく同じ構造の問題。** `000029` は隣り合う 2 行で
-- `repositoryUrl` と `feedbackUrl` の列 DEFAULT を設定しているが、meta 行を作るのは
-- GORM の `Create(&model.Meta{...})` で、`*string` の nil を **NULL として明示挿入する**
-- ため列 DEFAULT は効かない。#2700 は前者だけを直したので、こちらは NULL のまま残っていた。
--
-- **未設定は 2 通りある** (000084 と同じ)。
--
-- (a) NULL — mk-go 生まれの DB。新規行は internal/repository/meta.go の EnsureInitial 側で
--     入れるようにしたので、こちらは既存行だけが対象。
--
-- (b) 'https://github.com/misskey-dev/misskey/issues/new' — Misskey TS 生まれの DB
--     (drop-in 移行)。TypeORM は未指定の列に DEFAULT を書くので、TS が作った meta 行は
--     必ずこの値を持つ。**operator の申告ではなく列 DEFAULT の値**で、動いているのが
--     mk-go である以上、ソフトウェアへのフィードバックを Misskey 本体へ送らせることになる。
--
-- **これは「Misskey-TS が書いたデータは原則として保持する」の例外にあたる**
-- (docs/migration-from-ts.md の「破壊的なマイグレーション」に登録済み)。書き換えるのは
-- TypeORM の列 DEFAULT がそのまま残っている行だけで、operator が admin 画面で別の URL を
-- 設定していれば触らない。
--
-- **operator が意図的に空にした行も「多くの場合」埋まる。** admin 画面のブランディングが
-- 空文字を null に変換して送る (`branding.vue`) ので、「フィールドを空にする」操作の結果は
-- (a) と区別できない。**ただしこれはフロント側の変換で、サーバー側の正規化ではない** —
-- `repositoryUrl` と違い `feedbackUrl` には `isParsableURL` → nil の変換が無い
-- (`internal/api/admin/handler.go`) ので、API を直接叩いて空文字を書いた行は NULL にならず、
-- この UPDATE にも当たらない (= 埋まらない)。導線が無いよりあるほうがよいと判断して
-- 埋める側に倒す。別の URL を出したい operator は設定し直せる。
UPDATE "meta"
SET "feedbackUrl" = 'https://github.com/shiroha-a/mk/issues/new'
WHERE "feedbackUrl" IS NULL
   OR "feedbackUrl" = 'https://github.com/misskey-dev/misskey/issues/new';
