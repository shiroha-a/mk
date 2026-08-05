# Changelog

記法は Misskey 本家の `CHANGELOG.md` に準拠する (`General` / `Client` / `Server` の区分、`Feat` / `Enhance` / `Fix` の接頭辞)。
本家の新バージョンを取り込んだ分は「Misskey 202X.Y.Z に追従」の 1 行にまとめ、以降は mk-go 独自の変更のみを記載する。
`Client` は mk-go が同梱するフロントエンド (`third_party/misskey` fork) の変更を指す。

## 1.1.0

### Client

- Feat: リレーの設定画面に「リレー投稿の一時保存」と「リレー由来ユーザーの整理」を追加
- Feat: 大きなファイルを自動的に分割してアップロードするように
- Fix: 管理画面のジョブキューで、実在するキューが一覧に出ず、存在しないキューが常時ゼロ表示になっていた問題を修正

### Server

- Feat: リレー経由でしか届かない投稿と投稿者をデータベースに保存せず、一定時間で破棄するように (既定では無効)。反応があった時点で通常の投稿として保存する
- Feat: リレー由来の投稿者のうち、投稿もリアクションもフォロー関係も残っていないものを定期削除する機能を追加 (既定では無効)
- Feat: 大きなファイルの分割アップロード (`drive/files/create-chunked/*`) を実装 (既定では無効)
- Feat: オブジェクトストレージ上のファイル削除をジョブキュー化。大量削除で API の応答が待たされなくなり、ストレージの不調時に再試行するように
- Feat: アセットを同梱したコンテナイメージを追加し、`docker pull` してそのまま起動できるように
- Enhance: 添付ファイルの形式判定を Misskey 本家と同等にし、`.mov` / `.heic` など 13 形式が「不明なファイル」になる問題を解消
- Fix: オブジェクトストレージを設定してもローカルに保存され続け、`/files` が 404 になる問題を修正
- Fix: リモート投稿の定期削除が、クリップ・ピン留め・お気に入り・リアクションの付いた投稿まで削除していた問題を修正
- Fix: ノートのメンション数の上限がロールの設定を参照していなかった問題を修正
- Fix: 一部の定期処理が連合配送用のキューに積まれ、配送用のワーカーを消費していた問題を修正

## 1.0.0

### Note

- Misskey 2026.6.0 / 2026.7.0 に追従。
- 本バージョンをもって drop-in 互換 (同じ DB / Redis / フロントエンドを共有したままバックエンドだけを差し替えられる状態) をベースラインとして固定する。以降、同梱フロントエンドは mk-go 独自の変更を含む。

### General

- Feat: Misskey 本家との仕様差分カタログ (`docs/divergence.md`) を追加し、意図的に本家と異なる挙動を一覧化
- Enhance: mk-go と Misskey 本家に同じリクエストを投げてレスポンスを値レベルで突き合わせる差分比較 e2e を追加
- Enhance: Playwright による e2e (370 spec) を PR ごとに実行するように。Misskey TS バックエンドに対しては upstream 追従時に実行し、spec が mk-go の挙動に引きずられていないかを検証する
- Enhance: drop-in 互換の e2e も PR ごとに実行するように (いずれもマージはブロックしない)
- Enhance: DB スキーマ・マイグレーション記録・インデックス命名の drift を CI で検出するゲートを追加
- Enhance: API 互換性マトリクスを Misskey 2026.7.0 基準で再生成
- Enhance: `make help` で全ターゲットを一覧できるように。あわせて `make check` (整形・静的解析・テスト) や `make update` (更新手順) などの常用コマンドを追加
- Enhance: セットアップ手順とアップデート手順をドキュメントに整備
- Fix: 依存ライブラリの既知の脆弱性を解消 (`golang.org/x/image`・`x/crypto`・`x/net`)

### Client

- Feat: チャットとリバーシで、リモートユーザーを相手に選べるように (mk-go は連合に対応しているが、本家フロントエンドはローカル限定として UI 側で無効化していた)
- Feat: バージョン情報で mk-go の実装バージョンを表示するように
- Feat: 管理画面のジョブキューに、ワーカー数・オートスケールの状況・ディスパッチ待ちと処理時間の統計・スケール履歴を表示するように
- Fix: 管理画面で VAPID 鍵を自動生成したあと、生成された鍵が画面に反映されない問題を修正

### Server

- Feat: OAuth 2.0 / IndieAuth プロバイダ (`/oauth/authorize`・`/oauth/decision`・`/oauth/token`) を実装
- Feat: ActivityPub のコレクションエンドポイント (outbox・followers・following・featured) を実装
- Feat: 未実装だった通知タイプ (投稿通知・ログイン・トークン作成・テスト通知・ロール付与・チャットルーム招待) を実装
- Feat: 注目の投稿 (`/api/notes/featured`・`/api/users/featured-notes`) を、リアクションとリノートの時間窓ランキングで算出するように
- Feat: Mastodon 互換の `GET /api/v1/instance/peers` を実装
- Feat: `POST /api/notes` (公開投稿の一覧取得) を実装
- Feat: ローカルのブロック・プロフィール更新・ピン留めを ActivityPub で配信するように
- Feat: リレーへの配送に LD-Signature (RsaSignature2017) を付与するように
- Feat: 実績 (achievement) のサーバー側付与基盤を追加
- Feat: 定期メンテナンス処理を cron として追加 (期限切れミュートの掃除、モデレーター全員が非アクティブな場合の招待制への自動切替、各種クリーンアップ、リモート投稿の削除)
- Feat: フォロー / ブロックの一括処理をジョブキュー化
- Feat: チャンネルミュートに期限を設定できるように
- Feat: 長期間使われていないアンテナを自動で無効化するように
- Feat: ジョブキューのワーカー数・オートスケール状況・遅延の統計を `/api/admin/queue` から取得できるように (mk-go 独自 API)
- Enhance: API のレスポンス形状・エラーコード・パラメータ検証を Misskey 本家に揃える全面監査を実施し、全 API 領域で 300 件以上の差分を解消
- Enhance: 日時を返すフィールドを本家と同じミリ秒付き ISO-8601 形式に統一
- Enhance: 一覧系エンドポイントが返す埋め込みユーザーに、フォロー / ブロック / ミュート等の関係を付けるように
- Enhance: 検索・タイムライン・アンテナで、凍結済みユーザー・ブロック中のホスト・ミュート対象を除外するように
- Enhance: 配送に失敗し続けるインスタンスを自動でサスペンドするように
- Enhance: deliver / inbox ジョブのリトライ間隔を本家と同じ指数バックオフにし、既定の試行回数を deliver=12 / inbox=8 に (従来の no-retry 挙動が必要な場合は `deliverJobMaxAttempts` / `inboxJobMaxAttempts` に 1 を指定)
- Enhance: inbox の署名検証でリモート公開鍵のパース結果をキャッシュし、受信処理を高速化
- Enhance: タイムライン取得の N+1 を削減 (関係解決のバッチ化、リアクション取得の絞り込み)
- Enhance: `note.mentions` / `note.fileIds` / `clip_favorite` / `drive_file` の各カラムにインデックスを追加
- Fix: リノート数の集計条件が本家と異なり、自分の投稿を自分でリノートした場合や bot によるリノートも加算され、逆に引用リノートが加算されない問題を修正
- Fix: `/api/antennas/create` と `/api/admin/announcements/create` が本家の必須パラメータを検証せず、不完全なリクエストを受理する問題を修正
- Fix: `/api/admin/show-user` が本家のレスポンスに無いフィールドを多数返していた問題を修正
- Fix: `/api/meta` が管理者向けの設定値 (アプリアイコン URL・シングルユーザーモード) を公開エンドポイントで返していた問題を修正
- Fix: ユーザーの `updatedAt` が API リクエストのたびに更新され、本家の「最終投稿日時」と意味が異なっていた問題を修正 (「アクティブなユーザー」の絞り込みや更新日時順の並び替えに影響)
- Fix: 引用元・返信先として埋め込まれた投稿が閲覧者の公開範囲を無視して本文ごと返っており、フォロワー限定・ダイレクトの投稿が引用や返信を介して漏れる問題を修正
- Fix: 通知・Web Push・Webhook・ストリーミングに埋め込まれる投稿にも、受信者ごとの公開範囲チェックを適用 (2 段目の引用 / 返信まで)
- Fix: `/api/notes/show` が非可視の投稿の本文を返す問題を修正
- Fix: ドライブ・クリップ・ページ・Play・ギャラリー・リスト・アンテナ・ロール・チャンネルの各一覧で、公開範囲や所有者の検証が漏れていた問題を横断的に修正
- Fix: レガシーな `/api/signin` で 2 要素認証を迂回できる問題を修正
- Fix: ドライブファイルの配信でアップロード時の MIME をそのまま返しており、細工したファイルでスクリプトが実行されうる問題を修正 (オブジェクトストレージからの直接配信も同様)
- Fix: リモートのメディア URL が API レスポンスに生のまま含まれ、閲覧者の IP アドレスが外部サーバーへ漏れる問題を修正 (メディアプロキシ経由の URL へ書き換え)
- Fix: ActivityPub inbox で Digest ヘッダによる本文の完全性検証と、署名者のホストと `actor` の一致検証を行うように (なりすまし対策)
- Fix: 取得した ActivityPub オブジェクトの id ホストを取得元ホストに束縛し、他ホストのオブジェクトを詐称できないように
- Fix: HTTP Signature の `keyId` を actor のホストに束縛し、鍵の取り違えを防ぐように
- Fix: SSRF ガードの遮断範囲を、IPv6 の未指定アドレスや NAT64 / 6to4 等の特殊用途レンジまで拡張
- Fix: API と inbox に認証前のリクエストボディ上限を追加
- Fix: 管理 API に OAuth アクセストークンのスコープ検証を導入 (モデレーターが認可したアプリが、与えていない管理操作を実行できた)
- Fix: `/api/admin/roles/users`・`/api/admin/accounts/find-by-email`・`/api/app/show`・`/api/my/apps` からメールアドレスやアプリシークレットが漏れていた問題を修正
- Fix: ストリーミングで、匿名接続が認証必須チャンネルを購読できる問題と、1 接続あたりのチャンネル数に上限が無い問題を修正
- Fix: チャンネル・ハッシュタグ・ロールタイムライン・アンテナの WebSocket チャンネルにリアルタイム投稿が一切届かない問題を修正
- Fix: アンテナの WebSocket チャンネルで所有者の検証が無く、他人のアンテナを購読できた問題を修正
- Fix: リストのタイムラインで、メンバーごとの「返信を含める」設定やメンバーの追加・削除が反映されない問題を修正
- Fix: カスタム絵文字とアナウンスの作成・更新・削除を全接続へ配信するように
- Fix: 通知ストリームにミュート / ブロック / インスタンスミュートのフィルタを適用
- Fix: 連合先からの引用投稿で引用元が表示されず本文のみになる問題を修正
- Fix: 受信した投稿の公開範囲の導出が activity の `to` / `cc` を見ておらず、公開範囲を誤判定する問題を修正
- Fix: 同じリモート投稿が重複して保存され、引用元のリノート数が二重に増える問題を修正
- Fix: 送受信する ActivityPub のオブジェクト形状と検証を本家に揃える (Person の `url` とアイコンのフォールバック、添付ファイルのサイズ、Undo / Flag の必須フィールド、受信 Follow / Update / Announce / Reject の扱いなど)
- Fix: ブロックが原因でフォローを受け入れられない場合に Reject を返すように
- Fix: サイレンス中のインスタンスからの公開投稿をホーム扱いに降格するように
- Fix: 連合を無効にしている場合に ActivityPub の各エンドポイントと inbox を 403 で塞ぐように
- Fix: nodeinfo / WebFinger / host-meta の応答内容を本家に揃える
- Fix: ホストの比較を punycode 正規化してから行うようにし、大文字混じり / IDN のホストで照合に失敗する問題を修正
- Fix: サスペンド / ブロックしたインスタンスへの配送を停止するように
- Fix: `/api/i/delete-account` がアカウントを削除しても関連データが残り、連合先に Delete を配信していなかった問題を修正
- Fix: 管理画面の設定 (`/api/admin/meta`) が保存済みの値でなく既定値を返し、保存しても巻き戻る問題を修正
- Fix: 管理画面の CAPTCHA 設定がプロバイダ別の構造を返さず、設定を保存できない問題を修正
- Fix: `/api/admin/server-info`・`/api/admin/queue`・`/api/admin/abuse-user-reports`・`/api/admin/show-user`・`/api/admin/emoji`・`/api/admin/roles` のレスポンス形状と権限チェックを本家に揃える
- Fix: モデレーターが管理者の情報を閲覧できる問題と、一覧エンドポイント経由で管理者専用フィールドを取得できる問題を修正
- Fix: 下書き・ギャラリー・ページ・Play・クリップで、添付ファイルが解決されない・`isLiked` が欠ける・更新パラメータが無視される問題を修正
- Fix: ロール・カスタム絵文字・アバターデコレーションのレスポンスが内部モデルのままで、必要なフィールドを欠いていた問題を修正
- Fix: チャットのルーム参加に招待を必須化し、メンバー以外がルームの発言やメンバー一覧を読める問題を修正
- Fix: チャットの入力検証・リアクション・既読・ページネーション・権限を本家に揃える
- Fix: `/api/users/show` が凍結済みユーザーを非モデレーターにも返す問題を修正
- Fix: `/api/users/notes` の `withReplies` の既定値が本家と逆だった問題を修正
- Fix: `/api/i/update` が `alsoKnownAs` やフォロー / フォロワー一覧の公開範囲を無視する問題を修正
- Fix: `/api/i/notifications-grouped` がグループ化されていない結果を返す問題を修正
- Fix: `/api/charts/*` の `offset` の解釈が本家と異なり、集計窓の境界がずれる問題を修正
- Fix: リアクションで、投稿の受け入れ設定 (いいねのみ等)・センシティブな絵文字・ロール制限・メディアサイレンスが無視される問題を修正
- Fix: 検索 (`/api/notes/search`・`/api/users/search`・`/api/notes/search-by-tag`) の条件と並び順を本家に揃える
- Fix: スレッドミュートが保存されず機能していなかった問題を修正
- Fix: `/api/notes/polls/recommendation`・`/api/notes/clips` が常に空配列を返すスタブだった問題を実装で解消
- Fix: モデレーター / 管理者が他人の投稿を削除できない問題を修正
- Fix: ロールのキャッシュが期限切れの割り当てを延命し、期限後もロールが有効なままになる問題を修正
- Fix: 複数プロセス構成でインスタンス設定の変更が他プロセスに伝播しない問題を修正
- Fix: Misskey TS が作成した DB に対してマイグレーションが途中で停止する問題を修正 (2026.5.0 以降の TS 製 DB からの切替が完走しなかった)
- Fix: mk-go のマイグレーション記録が TypeORM の形式と食い違い、Misskey TS へ戻したときにマイグレーションが再実行される問題を修正
- Fix: mk-go でのみ作成されるカラムに依存していた箇所を外し、Misskey TS 製の DB でも動作するように
- Fix: 同じ定義のインデックスが mk-go 名と Misskey TS 名で二重に作成される問題を修正
- Fix: `useObjectStorage` を有効にしたインスタンスで、S3 移行前に保存されたローカルファイルへのアクセスが 404 になる問題を修正
- Fix: Misskey TS から移行した直後に、既に終了していたアンケートの終了通知が一斉に発火する問題を修正

## 0.9.2

### General

- Feat: Misskey 本家と mk-go の API 互換性マトリクスを自動生成するツールを追加
- Enhance: API 応答の shape drift を CI で検出するゲートを導入

### Server

- Feat: グループチャットの連合に対応 (招待の送受信と Accept / Reject、ルームメッセージの送受信、退出の同期)
- Feat: リモートアカウントの自己削除 (Delete) を処理し、tombstone 化とカスケード削除を行うように
- Feat: `/api/fetch-external-resources`・`/api/drive/files/upload-from-url`・`/api/notifications/create`・カスタム絵文字の zip エクスポート・`/api/hashtags/users` を実装
- Feat: `/miauth/:session/check` に対応し、サードパーティクライアントからのログインを可能に
- Feat: 管理画面のジョブキューダッシュボードを整備 (キューごとのメモリ / 接続数 / 稼働時間、completed / failed ジョブの一覧と作成・処理時刻、mk-go が扱わないキューの非表示、ユーザー詳細のサインイン履歴 / ロール割り当て表示)
- Enhance: 各 API のレスポンス形状を Misskey 本家に整合
- Enhance: エラー ID・HTTP ステータス・ページネーション上限・権限 / secure の扱いを Misskey 本家に合わせて整合
- Enhance: ハッシュタグの抽出を MFM パーサベースに変更
- Enhance: タイムライン取得を高速化 (note hydration の batch 化、添付ファイルの folder / owner 解決の batch 化、デフォルトロールポリシーの共有キャッシュ化)
- Enhance: 1 ページ目のタイムライン応答を viewer ごとに短時間キャッシュするオプションを追加 (既定で無効)
- Enhance: ジョブキューの挙動を改善 (completed / failed の保持設定、Retry / Scheduled の分離、重複 Like の冪等化、配列でラップされた inbound activity の許容、キューごとの既定並列度の調整)
- Fix: サードパーティクライアント (Miria など) で、プロフィール・リアクション・リスト等のタブがレスポンス形状の不一致でクラッシュする問題を修正
- Fix: データエクスポート完了通知の `exportedEntity` が不正な値になり、通知一覧の取得が失敗する問題を修正
- Fix: 投稿通知を設定したユーザーへの返信がタイムラインから除外される問題を修正
- Fix: 実績の獲得 (achievementEarned) 通知が発火しない問題を修正
- Fix: プロフィール更新時にタグが空だと更新全体が失敗する問題を修正
- Fix: 連合インスタンス一覧の blocked / silenced フィルタと並び順を Misskey 本家に合わせて修正
- Fix: フォロー / アンフォローがストリームのフォロー関係に即時反映されない問題を修正
- Fix: `/api/users/reactions` の公開範囲フィルタと、リモートユーザーでのエラーを修正
- Fix: `/api/users/lists/show` で非公開のリストが他者に露出する問題を修正

## 0.9.1 以前

### Note

- リリース単位ではなく Phase 単位で開発していた期間。個別の変更は GitHub の issue / PR に記録されているため、ここでは主な成果のみをまとめる。
- Misskey 2026.5.4 に追従。

### General

- Feat: Misskey TS インスタンスとの drop-in 互換を検証する e2e 基盤を整備し (pytest / Cypress / Playwright)、nightly CI で実行するように
- Feat: 本家との差分を追跡するドキュメント運用を開始 (`docs/update/`)

### Server

- Feat: HTTP サーバー・DB / Redis 接続・設定ローダー・マイグレーション等の基盤を構築
- Feat: ユーザー・投稿・タイムライン・ドライブ・フォロー・リアクション・通知を実装
- Feat: WebSocket ストリーミングの全チャンネルを実装
- Feat: ActivityPub の送受信を実装 (Inbox・Deliver・Renderer・Resolver・HTTP Signature)
- Feat: ジョブキュー・検索 (Meilisearch / SQL フォールバック)・チャート・管理 API を実装
- Feat: TOTP による 2 要素認証・パスワードリセット・MiAuth・アプリ API を実装
- Feat: HTTP Signature の Ed25519 (FEP-521a Multikey) による署名・検証に対応 (Misskey 本家に先行する mk-go 独自実装、既存の RSA 鍵と併用)
- Feat: 受信する ActivityPub アクティビティの LD-Signature (RsaSignature2017) 検証を実装
- Feat: リモートユーザーの投稿数 / フォロー数 / フォロワー数を連合元から取得して表示するように (mk-go 独自)
- Enhance: ジョブキューのドライバを mkq に移行し、既定ドライバに
- Enhance: inbox の署名検証をワーカー側に移し、受信スループットを Misskey TS 比 2.6-2.8 倍に
- Enhance: 一覧系エンドポイントのページネーションを整理し、`sinceDate` / `untilDate` を全 61 ハンドラで受け付けるように
- Fix: Playwright / drop-in e2e で検出した Misskey 本家との差分 40 件以上を解消
- Fix: 遅延配送されたリモート投稿の投稿日時が受信時刻になる問題を修正
- Fix: カスタム絵文字のアニメーションがメディアプロキシで静止画化される問題を修正
- Fix: URL プレビューで Shift_JIS / EUC-JP / ISO-2022-JP のページが文字化けする問題を修正
- Fix: リモートユーザーのアクティビティタブでヒートマップが空になり、投稿数の折れ線が負方向にのみ動く問題を修正
