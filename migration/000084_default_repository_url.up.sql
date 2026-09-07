-- meta.repositoryUrl が「未設定」のインスタンスで、frontend の /about-mkgo と
-- MkSourceCodeAvailablePopup がソースコードの案内を出せるようにする。AGPL-3.0
-- section 13 が求める「動いているコードに対応するソース」の案内が既定で存在
-- しない状態を塞ぐ (#2700)。
--
-- **未設定は 2 通りある。**
--
-- (a) NULL — mk-go 生まれの DB。000029 が列 DEFAULT を upstream 互換の
--     'https://github.com/misskey-dev/misskey' に設定しているが、meta 行を作るのは
--     GORM の `Create(&model.Meta{...})` で、`*string` の nil を **NULL として明示
--     挿入する**ため列 DEFAULT が効かない。新規行は internal/repository/meta.go の
--     EnsureInitial 側で入れるようにしたので、こちらは既存行だけが対象。
--
-- (b) 'https://github.com/misskey-dev/misskey' — Misskey TS 生まれの DB
--     (drop-in 移行)。TypeORM は未指定の列に DEFAULT を書くので、TS が作った meta 行は
--     必ずこの値を持つ。**operator の申告ではなく列 DEFAULT の値**であり、動いて
--     いるのが mk-go である以上「このサーバーのコード」として Misskey 本体を案内するのは
--     誤りになる。さらに frontend は `repositoryUrl !== 'https://github.com/misskey-dev/misskey'`
--     で改変版の告知ポップアップを出すか決めるので、この値のままだと**告知が出ない**。
--
-- **これは「Misskey-TS が書いたデータは原則として保持する」の例外にあたる**
-- (docs/migration-from-ts.md の「破壊的なマイグレーション」に登録済み)。書き換える
-- のは TypeORM の列 DEFAULT がそのまま残っている行だけで、operator が admin 画面で
-- 別の URL を設定していれば触らない。
--
-- **`feedbackUrl` は触らない。** 000029 は `repositoryUrl` と同じ理由で
-- `feedbackUrl` にも列 DEFAULT を入れており、そちらも GORM 経由では効かないので
-- 新規インスタンスでは NULL のまま (フィードバックの導線が出ない)。ただし AGPL 13 条の
-- 案内とは無関係なので、この migration の範囲には入れない。
--
-- **operator が意図的に空にした行も埋まる。** admin/update-meta は妥当な絶対 URL で
-- なければ NULL を書く (upstream の `URL.canParse` と同じ) ので、「フィールドを空に
-- する」操作の結果は (a) と区別できない。AGPL 13 条の観点では案内が無い状態のほうが
-- 問題なので、埋める側に倒す。別の URL を出したい operator は admin 画面で設定し直せる。
UPDATE "meta"
SET "repositoryUrl" = 'https://github.com/shiroha-a/mk'
WHERE "repositoryUrl" IS NULL
   OR "repositoryUrl" = 'https://github.com/misskey-dev/misskey';
