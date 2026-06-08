# Changelog

## Unreleased

- Fix: `/api/channels/update` でモデレーターが他人のチャンネルを編集できず、`channels/create` ・ `update` が `allowRenoteToExternal` パラメータを無視していた問題を修正 (issue #1540 HIGH)。(1) 本家 `channels/update` は owner 以外でも moderator なら編集を許可する (`!isModerator && channel.userId !== me.id` で reject)。mk-go は owner のみだったので、既存 `roleService.IsModerator` を流用した moderator bypass を `Service.Update` に追加 (checker 未配線時は owner-only に fail-closed)。(2) 本家 `create.ts:93` の `allowRenoteToExternal ?? true` 準拠で `create`/`update` が `allowRenoteToExternal` (bool) を受理し channel に保存する (未指定は true 既定)。`model.Channel.AllowRenoteToExternal` カラムは既存のため migration 不要。
- Fix(security): `/api/antennas/notes` ・ `/api/roles/notes` が viewer の mute/block/channel-mute を無視し、`roles/notes` が非 explorable role でも note を返していた問題を修正 (issue #1544 HIGH)。本家は両エンドポイントで viewer が mute した user / viewer を block している user / mute した channel の note を除外し、`roles/notes` は対象 role が `isExplorable=false` なら空配列を返す。mk-go はこれらの除外/ガードを欠いていた。`core/notesfilter` に `LoadMuteBlockSets` (muted-user / blocker / muted-channel を各 1 query で解決、repo error は fail-closed で 500) + `ApplyMuteBlockChannel` (note/reply/renote の author と channelId/renoteChannelId で in-memory 除外) を追加し両 handler に配線。`roles/notes` に `isExplorable=false` 空配列ガードを追加。注: `generateBaseNoteFilteringQuery` の muted-instances / blocked-host / suspended-user 次元と nested renote の深い author 除外は本 issue スコープ外 (別 follow-up)。

- Fix: `/api/reversi/show-game` ・ `match` ・ `verify` のレスポンス欠落フィールドと error code の UUID 不一致を修正 (issue #1553 HIGH)。(1) 本家 `ReversiGameDetailed` の `form1`/`form2` 等のフィールドが pack されておらず欠落していたので本家 `ReversiGameEntityService` 準拠で追加。(2) reversi 各エンドポイントの error code 4 件が本家と異なる UUID だったため本家の UUID に揃える。parity 用途 (security 影響なし)。

- Fix(security): `/api/admin/roles/assign` ・ `/unassign` でモデレーターの権限チェックと `expiresAt` の型が本家と異なっていた問題を修正 (issue #1542 HIGH)。(1) 本家はモデレーター (非 admin) が role を assign/unassign する際、対象 role の `canEditMembersByModerator` が true でなければ拒否する (admin は常に可)。mk-go はこのゲートが無く、任意のモデレーターが任意 role を付け外しできた。(2) 本家 `assign` の `expiresAt` は epoch ms (number) を受ける (assign.ts) が、mk-go は RFC3339 文字列を期待していた。本家準拠で epoch ms を受理する型に修正。権限判定は既存 roleService の admin/moderator 判定を流用し fail-closed。

- Fix: `/api/i/update` が `followingVisibility` / `followersVisibility` パラメータを無視していた問題を修正 (issue #1546 HIGH)。本家 `i/update` は両 param (`public` / `followers` / `private`) を受けて user に保存する (update.ts paramDef)。mk-go は `users/show` 等に `followersVisibility` gate (#1461) があるのに値を設定できず、フォロー/フォロワー一覧の公開範囲を変更できなかった。本家準拠で両 param を受理し対応カラムへ保存、enum 値も本家と同じく検証する (不正値は弾く)。model に既存のフィールド/型を使用 (DB migration 不要)。

- Fix: `/api/notes/children` が pure renote (本文/ファイル/投票を持たない単純リノート) をスレッドの子として返していた問題を修正 (issue #1554)。本家 `notes/children` は renoteId 分岐を `AND (text IS NOT NULL OR fileIds != '{}' OR hasPoll = TRUE)` で括り、pure renote をスレッド children から除外する (reply と quote renote のみ children に出る; children.ts)。mk-go の `repository.ListChildrenOf` は `("replyId" = ? OR "renoteId" = ?)` に content guard が無く pure renote を漏らしていた。renote 分岐に既存の pure-renote 述語 (`text IS NULL AND fileIds = '{}' AND hasPoll = false` 相当) を適用し本家に揃える (reply 分岐は空 reply も子なので guard 無しのまま)。`MockNoteRepository.ListChildrenOf` にも同ロジックを反映。可視性 push-down (#1500) / block / mute は不変。

- Fix(stream): WebSocket `antenna` channel が live note を1件も受信していなかった問題を修正し、push 後の stale-narrowing に対する per-subscriber 可視性 re-filter を追加 (issue #1573, #1549 の antenna HIGH / #1569 follow-up)。antenna の match 配信は `core/antenna.Service.OnNoteCreated` が Redis Stream へ `XAdd` するだけ (REST `antennas/notes` 用) で、`AntennaChannel` が Subscribe する pubsub topic `antennaTimeline:<id>` へ publish していなかった (Stream と Pub/Sub は別プリミティブ)。本家 `AntennaService.addNoteToAntenna` の `xadd` + `publishAntennaStream` 二系統に合わせ、match した note を `StreamingPublisher.PublishNote(streamKey(antennaId), n, author)` で publish する (`NotePublisher` を注入、未配線時は XAdd のみで realtime skip = 旧挙動)。あわせて `AntennaChannel.OnRedisEvent` を channel/hashtag/role と同じ canonical pipeline (`streamNoteVisibleForViewer` fail-closed re-filter → `filter.shouldEmit` hardmute/renote/file → `hideEmbeds` (#1536 維持) → Send) に揃え、push 時の owner `CanSeeNote` (#1464) が著者の followers 化・owner の unfollow で stale 化した場合の漏洩を per-subscriber で塞ぐ。#1569 の所有者ゲートは維持。

- Fix: `/api/users/notes` の `withReplies` デフォルトが本家と逆 (true) になっていた問題と、`withReplies && withFiles` 同時指定エラーの欠落を修正 (issue #1547 HIGH)。本家 `users/notes` の paramDef は `withReplies` default `false` (notes.ts:59) だが、mk-go は `req.WithReplies==nil` 時に `true` を既定にしていた (コメントも誤って『upstream default=true』と記載)。default を `false` に修正 (返信は明示指定時のみ含む)。あわせて本家 `notes.ts:42-45` の `withReplies && withFiles` 同時指定で投げる `BOTH_WITH_REPLIES_AND_WITH_FILES` (id `91c8cb9f-36ed-46e7-9ca2-7df96ed6e222`) を追加。

- Fix(security): `/api/users/show` が suspend 済みユーザーを非モデレーターにも素通しで返していた問題を修正 (issue #1547 HIGH)。本家 `users/show` は単体モード (userId/username) で `!isModerator && user.isSuspended` のとき `NO_SUCH_USER` (404) を返し (show.ts:173-175)、バルクモード (userIds) では非モデレーターに `isSuspended=false` を強制して suspended を結果から除外する (show.ts:136-141)。mk-go は両モードとも suspended を素通ししていた。既存の `moderatorChecker` (users/reactions 用に配線済み) を流用して接続 viewer のモデレーター判定を行い、非モデレーターには単体で `NO_SUCH_USER`、バルクで除外を適用する。モデレーターは従来どおり閲覧可。匿名 / checker 未配線は `iAmModerator=false` で fail-closed。

- Fix: `/api/charts/*` および `/api/charts/user/*` の全12エンドポイントを本家Misskeyのcharts API契約に揃えた (issue #1565)。(1) `offset` パラメータを本家同様epoch-msのカーソル (`ps.offset ? new Date(ps.offset) : null` 相当でウィンドウ末尾を指す絶対タイムスタンプ、0/null はカーソル無し、負値も受理) として解釈するよう修正。従来は「now()から引くspan数」と誤解釈し負値を拒否していた (`offset` を `*int64` 化)。あわせて chart engine が cursor 指定時のウィンドウ末尾バケットを本家 `getChartRaw` (`truncate(cursor + 1span - 1ms)` = ceil) ではなく floor で求めていた off-by-one も修正 (非境界の offset で末尾バケットが1つずれる問題、cursor 経路のみ ceil 化し now 経路の floor は維持)。(2) schema validation 失敗時の `INVALID_PARAM` エラーを本家の正しい id `3d81ceae-475f-4600-b2a8-2bc116157532` (従来は非本家の hardcoded id) にし、本家 `endpoint-base.ts`/`ApiCallService` 同様 `kind: "client"` と `info: {param, reason}` を付与 (`apierr.InvalidParamClient`)。info の param/reason は ajv 非搭載のため best-effort。(3) 未認証 GET の成功応答に本家 `cacheSec` 相当の `Cache-Control: public, max-age=3600` を付与 (本家同様 POST/認証済み GET/エラー応答には付けない)。注: client-kind エラーの `WWW-Authenticate` ヘッダと、不正JSON body時の本家Fastify parse error envelope 化は apierr 横断の別スコープ (follow-up)。

- Fix(security): Web Push 通知 payload に埋め込まれる note の depth-2 renote/reply embed が受信者可視性で hide されておらず、通知 note が引用/返信した非可視 note (followers / specified) の本文が push 経由で漏れていた問題を修正 (issue #1575, #1572/#1568 follow-up)。#1572 (PR #1574) で webpush の top-level `CanSeeNote` gate は入れたが、REST `i/notifications` (`notehide.HideNotificationNotes`, #1570) / stream 通知 (`hideNotificationNote`, #1570) が持つ depth-2 embed hide が webpush だけ欠けて3経路がずれていた。`webpush.NoteRepoPacker.PackNoteByID` で `entity.PackNote` 後の note の renote/reply embed を、受信者 (notifiee) から `core/note.HideEmbedDecision` で見えない場合に `entity.HideNoteEntity` で blank する。embed 著者の follow 判定は 1 回の `FilterFollowingsFromAnchor` でバッチ解決。`core/webpush` は `api/notehide` を import 不可のため core helper を直接使用 (duplication 解消は別 issue)。fail-closed: `followingRepo` 未配線 / viewer 空 / クエリ失敗は followers・specified embed を hide。
- Fix: `/api/notes/reactions/create` が note の `reactionAcceptance` (likeOnly 等)・絵文字の sensitive / role 制限・media-silenced ホストを無視して任意のカスタム絵文字を保存していた問題を修正 (issue #1538)。本家 `ReactionService.create` 準拠で、`likeOnly` および remote リアクターに対する `likeOnlyForRemote` / `nonSensitiveOnlyForLocalLikeOnlyForRemote` は ❤ に強制、カスタム絵文字は (a) 使用可能 role を持たない role 限定絵文字、(b) media-silenced な remote ホスト、(c) `nonSensitiveOnly` 系での sensitive 絵文字 のいずれかで ❤ にフォールバックする。判定は `reaction_service.Create` 内に集約 (local / 連合 inbound 双方が通る)。role lookup (`UserRolesProvider`=role.Service) と media-silence (`MediaSilenceChecker`=instance.Service の新 `IsMediaSilenced`) は setter 注入で、`reactionAcceptance` 未設定 + dep 未配線時は従来挙動を維持する。
- Fix: `/api/notes/show-partial-bulk` のレスポンスが完全な `Note` 配列で、本家の軽量 diff `{id, reactions, reactionEmojis}` (reaction polling 用途) と shape が異なっていた問題を修正 (issue #1538)。`packMany` が解決済みの reactions (buffered merge 込み) / reactionEmojis を投影して本家 shape に揃える。なお本家 `show-partial-bulk` は可視性 filter を持たない (anonymous endpoint) が、進行中の可視性 IDOR sweep と整合させるため mk-go では `FilterVisible` を**維持**する (shape のみ本家化、意図的な divergence; #509 の hardening を保つ)。
- Fix: `/api/notes/delete` でモデレーター/管理者が他人のノートを削除できなかった問題を修正 (issue #1538)。旧実装は `note.userId != actor.id` で無条件に `ACCESS_DENIED` を返し、本家 `NoteDeleteService` の `isModerator` bypass が無かった。`DeleteService.DeleteAs(actor, isModerator, noteID)` を追加し、`!isModerator && note.UserID != actor.ID` のときのみ拒否する。モデレーター削除時は note の**著者**を `userRepo` で解決して federation Delete / timeline fanout の hook に渡し、AP Delete の attribution と home TL 撤去が著者基準になるようにする (federation hook は remote 著者を `IsLocal()` で配信スキップ済み)。handler は `roleService.IsModerator` で判定 (checker 未配線は著者のみ)。`Delete(user, noteID)` は `DeleteAs(user, false, noteID)` の薄いラッパとして既存呼出 (inbound federation 経路含む) を維持。注: モデレーション監査ログ (`deleteNote`) は別 follow-up。
- Fix: `/api/notes/polls/recommendation` が常に空配列を返すスタブだった問題を実装で修正 (issue #1538)。本家 `recommendation.ts` 準拠で、ローカル (userHost IS NULL) public かつ未期限切れの poll のうち、閲覧者自身の投稿でない / 未投票 (NOT EXISTS poll_vote) / 投稿者を mute していないものを `noteId` DESC で返す (`excludeChannels` でチャンネル poll を除外可、limit 既定10/上限100 + offset)。`PollRepository.ListRecommendation` を追加し handler に配線。可視性は public 限定なので追加 gate 不要。
- Fix: `/api/notes/thread-muting/create` ・ `/delete` が noteId 検証後に 204 を返すだけでスレッドミュートを永続化していなかった問題を修正 (issue #1538)。`notes/state` の `isMutedThread` 読み取り経路は実装済み (`note_thread_muting` を引く) だったため、write 側が stub なことでスレッドミュートが一切機能していなかった。本家 `thread-muting/{create,delete}.ts` 同様に note を解決し (可視性 gate 無し、不在は `NO_SUCH_NOTE`)、`threadId`(無ければ note id)で `note_thread_muting` 行を冪等に作成/削除する (`NoteThreadMutingRepository` を notes handler に配線)。
- Fix(stream): hashtag の WebSocket チャンネルが live note を1件も受信しておらず、かつ q の複数タグ (AND グループ / OR グループ) を無視して第1タグのみ購読していた問題を修正 (issue #1549 のうち hashtag 分)。`hashtag:<tag>` への publisher が存在しなかった。`FanoutHook.OnNoteCreated` で note.Tags の各タグを `searchnorm.Normalize` (NFKC+lower) して `hashtag:<normalized>` へ publish (重複正規化キーは1回)。consumer (`HashtagChannel`) は q を正規化して全グループの全タグの distinct キーを購読し (旧実装は `q[0][0]` のみ)、`OnRedisEvent` で payload の tags に対し本家 `q.some(group => group.every(tag => …))` の OR-of-ANDs を再評価 + noteId で重複配信を dedupe (複数 subscribed tag topic 経由の二重到着対策) + 受信者可視性 (`streamNoteVisibleForViewer`) + 既存 filter + embed hide。正規化は publish/subscribe 両側で揃える。

- Fix(stream): roleTimeline の WebSocket チャンネルが live note を1件も受信していなかった問題を修正 (issue #1549 のうち roleTimeline 分)。`roleTimeline:<roleId>` への publisher が存在しなかった。`FanoutHook.OnNoteCreated` で public note を著者の各ロールの `roleTimeline:<roleId>` へ publish し (`UserRolesLookup` 経由)、consumer (`RoleTimelineChannel`) で本家 `role-timeline.ts` 準拠に **isExplorable role かつ visibility==public のみ** emit する (isExplorable は runtime 可変なので per-event check; checker 未配線は fail-closed)。`role.Service.IsExplorable` を追加し、channel は factory (`NewRoleTimelineFactory(roleService)`) で checker を注入。publish は public note に絞って無駄な fanout を回避。

- Fix(stream): Misskey チャンネル (channel) タイムラインの WebSocket チャンネルが live note を1件も受信していなかった問題を修正 (issue #1549 のうち channel 分)。fanout が `homeTimeline`/`localTimeline`/`globalTimeline`/`userListTimeline` の4 topic にしか pubsub publish せず、`channel:<channelId>` への publisher が存在しなかった (core/channel.OnNotePosted は counter 更新のみ)。`FanoutHook.OnNoteCreated` で note.channelId があれば `channel:<id>` へ publish するようにし、consumer 側 (`ChannelTimelineChannel.OnRedisEvent`) で channelId 一致 + 受信者可視性 (`streamNoteVisibleForViewer`、本家 `isNoteVisibleForMe` 相当の fail-closed gate) + 既存 hardmute/renote/file filter + embed hide を通す。可視性 gate は新規 channel が live 化する際の followers/specified leak を防ぐ防御層 (parse 失敗・nil snapshot は drop)。注: 完全な mute/block parity (instance-mute/user-mute/block/renote-mute) は別スコープ。hashtag/roleTimeline/antenna の同種 (live note 不達) は #1549 の別 PR で対応。
- Fix(stream): roleTimeline の WebSocket チャンネルが live note を1件も受信していなかった問題を修正 (issue #1549 のうち roleTimeline 分)。`roleTimeline:<roleId>` への publisher が存在しなかった。`FanoutHook.OnNoteCreated` で public note を著者の各ロールの `roleTimeline:<roleId>` へ publish し (`UserRolesLookup` 経由)、consumer (`RoleTimelineChannel`) で本家 `role-timeline.ts` 準拠に **isExplorable role かつ visibility==public のみ** emit する (isExplorable は runtime 可変なので per-event check; checker 未配線は fail-closed)。`role.Service.IsExplorable` を追加し、channel は factory (`NewRoleTimelineFactory(roleService)`) で checker を注入。publish は public note に絞って無駄な fanout を回避。

- Fix(stream): Misskey チャンネル (channel) タイムラインの WebSocket チャンネルが live note を1件も受信していなかった問題を修正 (issue #1549 のうち channel 分)。fanout が `homeTimeline`/`localTimeline`/`globalTimeline`/`userListTimeline` の4 topic にしか pubsub publish せず、`channel:<channelId>` への publisher が存在しなかった (core/channel.OnNotePosted は counter 更新のみ)。`FanoutHook.OnNoteCreated` で note.channelId があれば `channel:<id>` へ publish するようにし、consumer 側 (`ChannelTimelineChannel.OnRedisEvent`) で channelId 一致 + 受信者可視性 (`streamNoteVisibleForViewer`、本家 `isNoteVisibleForMe` 相当の fail-closed gate) + 既存 hardmute/renote/file filter + embed hide を通す。可視性 gate は新規 channel が live 化する際の followers/specified leak を防ぐ防御層 (parse 失敗・nil snapshot は drop)。注: 完全な mute/block parity (instance-mute/user-mute/block/renote-mute) は別スコープ。hashtag/roleTimeline/antenna の同種 (live note 不達) は #1549 の別 PR で対応。
- Fix(stream): Misskey チャンネル (channel) タイムラインの WebSocket チャンネルが live note を1件も受信していなかった問題を修正 (issue #1549 のうち channel 分)。fanout が `homeTimeline`/`localTimeline`/`globalTimeline`/`userListTimeline` の4 topic にしか pubsub publish せず、`channel:<channelId>` への publisher が存在しなかった (core/channel.OnNotePosted は counter 更新のみ)。`FanoutHook.OnNoteCreated` で note.channelId があれば `channel:<id>` へ publish するようにし、consumer 側 (`ChannelTimelineChannel.OnRedisEvent`) で channelId 一致 + 受信者可視性 (`streamNoteVisibleForViewer`、本家 `isNoteVisibleForMe` 相当の fail-closed gate) + 既存 hardmute/renote/file filter + embed hide を通す。可視性 gate は新規 channel が live 化する際の followers/specified leak を防ぐ防御層 (parse 失敗・nil snapshot は drop)。注: 完全な mute/block parity (instance-mute/user-mute/block/renote-mute) は別スコープ。hashtag/roleTimeline/antenna の同種 (live note 不達) は #1549 の別 PR で対応。
- Fix(security): Web Push 通知 payload に埋め込まれる note が受信者の可視性で gate されておらず、非可視 note (followers / specified) が push 経由で漏れうる問題を修正 (issue #1572)。`webpush.NoteRepoPacker.PackNoteByID` は `entity.PackNote` で full pack するだけで、REST `i/notifications` (`FilterVisible`, #1444) / stream 通知 (`noteVisibleToNotifiee`, #1471) が持つ受信者可視性 gate を欠いていた。受信者 (notifiee) を viewer として `core/note.CanSeeNote` で gate し、見えない note は detail を載せず `noteId` だけ残す (両 sibling と同 shape)。`notifieeID` を `buildPushBody` → `NotePacker.PackNoteByID(noteID, viewerID)` に thread し、`NoteRepoPacker` に `FollowingRepository` を注入 (未配線 / 空 viewerID は followers・specified を fail-closed、public・home は常時 pass)。注: 通知に埋め込まれた note の depth-2 renote/reply embed hide は REST/stream と揃えて別途対応 (#1568 の通知 embed hide と同系)。

- Fix(security): WebSocket `antenna` channel が antenna の所有者検証なしに購読を受け付けていた cross-user IDOR を修正 (issue #1569)。`AntennaChannel.Init` は client の `antennaId` を受け取って `antennaTimeline:<id>` を購読するだけで、接続中の user がその antenna を所有しているか検証していなかった。antenna stream は push 段で所有者可視性 (`matchNote`→`CanSeeNote`, #1464) でのみ gate されるため、任意の認証 user が他人の antenna を購読すると、所有者には見えるが自分には見えない followers / specified note を top-level で受信できた (REST `antennas/notes` は `Service.Show` で所有者検証済み、WS だけ非対称だった)。本家 `AntennaChannel.init()` の `antenna.userId === user.id` 検証に相当する所有者ゲートを `Init` に追加 (factory 経由で `AntennaRepository` を注入、未配線時は fail-closed)。注: antenna fanout は現状 Redis Stream (XADD) で配信され dispatcher の pub/sub topic には publish されないため本 leak は現時点では潜在的 (delivery path 接続時に顕在化)。stale-visibility re-filter と antenna realtime delivery path は別 issue。

- Fix(security): hideNote 相当のゲートを #1536 の embed 限定から拡張し、(1) top-level note への著者設定ゲートと (2) 通知 (notifications / main stream) に埋め込まれた note の embed をカバー (issue #1568, #1536 follow-up)。top-level は `core/note.HideNoteByPrefsDecision` で著者設定のみ (本家 `treatVisibility` の `makeNotesFollowersOnlyBefore` 降格・`makeNotesHiddenBefore`・`requireSigninToViewContents`・own-note 短絡) を適用し、intrinsic な followers/specified の hide は従来どおり `CanSeeNote`/`FilterVisible`/SQL push-down/notes-show の ID 既知 doctrine (#799 / #1488) に委ねる (再 blank しない)。これで「public note が著者の `makeNotesFollowersOnlyBefore` 期限切れで非フォロワーに本文表示」「`makeNotesHiddenBefore` 期限切れの古い note 表示」「`requireSigninToViewContents` が匿名に効かない」3 leak を top-level でも塞ぐ。通知は REST `i/notifications` (`notehide.HideNotificationNotes` を `FilterVisible` 後に適用、note 自身の著者設定 + depth-2 renote/reply embed を 1 ページ 1 query で hide) と streaming (`NotificationsChannel` / `MainChannel` の bare 通知 body と reply/renote/mention envelope を per-connection snapshot で hide、隠すものが無ければ verbatim 転送) の双方を gate。hide は本家 `pack()` 同様すべて `entity.HideNoteEntity` による in-place blank (行 filter はしない)。

- Fix(security): API レスポンスやストリーミングに埋め込まれる引用元/返信先 (embedded renote/reply) が、閲覧者の可視性に関係なく本文ごと返っており、フォロワー限定・ダイレクト投稿を引用/返信した投稿経由で非可視 note の本文が漏れていた問題を修正 (issue #1536)。本家 `NoteEntityService.hideNote` 相当を導入し、embed が viewer から見えない場合は `text`/`cw`/`poll`/`files`/`fileIds`/`visibleUserIds` を空にして `isHidden=true` を立てる (id/user/リアクション数等の枠は残す)。判定は本家 `treatVisibility`+`shouldHideNote` を移植した `core/note.HideEmbedDecision` (public/home は著者の `makeNotesFollowersOnlyBefore` 期限切れで followers 降格、own/specified-recipient/mention/follower は可視、`requireSigninToViewContents`・`makeNotesHiddenBefore` も評価) に集約。REST は note を返す全 surface (timeline・notes/show・renotes・replies・children・users/notes・antennas/notes・channels/timeline・clips/notes・roles/timeline・drive/files notes・featured-notes・reactions・i/favorites・pinned 等) を共通の `api/notehide` 経由で 1 ページ 1 回の batch follow クエリ (`FilterFollowingsFromAnchor`) で判定、ストリーミングは各 WebSocket connection の in-memory following snapshot で per-subscriber に判定し、隠す embed が無い payload は body を verbatim 転送 (publisher の body cache を非破壊で温存)。注: top-level note 側の `makeNotes*Before`/`requireSignin` 適用・通知 (notifications/main stream) に埋め込まれた note の embed・antenna の top-level leak は別スコープ (follow-up)。

- Fix(security): 連合受信した引用ノートが、引用先のローカル note を可視性無視で解決・紐付けていたため、リモートが任意のローカル note (followers-only / specified(DM) 含む) を引用先に指定すると、その非可視 note が `renote` embed 経由で本来見られない viewer へ broadcast される IDOR を修正 (issue #1534 / #1532 regression)。本家 `NoteCreateService` (他人の followers note と全 specified note を renote 対象から reject) 準拠で、inbound quote 解決時に引用先が followers / specified の場合は紐付けず degrade する (`renoteCount` 増分も skip)。なお embedded renote/reply が packing 共通層で viewer 可視性に応じて hide されない根本課題は #1536 で追跡。

- Fix: 連合 note 取り込みの dedup race で同一リモート note が重複保存され、引用元の `renoteCount` が二重増分されうる問題を修正 (#1527 follow-up)。本家同様 `note.uri` に部分 UNIQUE index (`uri IS NOT NULL`) を追加 (migration 000056 で既存重複行を最小 id を残して除去 → 000057 で最大テーブル方針に従い `CREATE UNIQUE INDEX CONCURRENTLY`) し、`IngestNote` の `Create` が UNIQUE 制約違反になった場合は既存行を引いて dedup hit 扱い (created=false) にして重複 INSERT と hook / `renoteCount` の二重発火を防ぐ。

- Fix: 連合先からの引用ノート (quote renote) の引用元がノートカードに表示されず本文のみになっていた問題を修正 (issue #1527)。inbound の ActivityPub Note 取り込み (`IngestNote`) が `_misskey_quote` / `quoteUrl` を読まず `renoteId` を立てていなかったため、リモートの引用が「引用元なし」で保存されていた。本家 `ApNoteService` 同様に両 URI を順に解決して引用元 note を紐付け (local / 取り込み済みは fetch 無し、未知は fetch、quote サイクルは in-flight guard で遮断)、引用元の `renoteCount` も本家 `NoteCreateService` 準拠で increment する (自己引用・bot は除外)。

- Fix: リモート(連合先)のメディア URL が API レスポンスに生のまま含まれ、フロントエンドがそれらを直接 `<img src>` で取得することで閲覧者の IP アドレスが外部サーバーへ漏洩していた問題を修正 (issue #1529)。pack 時に remote origin を判定して media proxy 経由 URL へ書き換える server 側書き換え層 (`entity.MediaURLContext`) を追加し、本家 `DriveFileEntityService` の `getPublicUrl`/`getThumbnailUrl` 相当を server 側で行う。対象はフロントエンドが verbatim 描画する surface: ドライブファイル(ノート添付の `url`/`thumbnailUrl`/`webpublicUrl`)・ユーザーのアバター/バナー・チャンネルバナー・ロールの `iconUrl`・アナウンスの `imageUrl`・URL プレビューのサムネイル/アイコン・`/api/federation/users` のアバター・`/api/admin/drive/show` (モデレーターの IP 保護)。`url` フィールドは image MIME に限定して書き換え (proxy の passThrough が非 browsersafe MIME を拒否するため、PDF/zip 等のダウンロードを壊さない)、サムネイルは static mode。`proxyRemoteFiles` 設定を尊重し(アバターは本家同様常時プロキシ)、内部メディアプロキシ利用時は HMAC 署名付き URL を生成 (`mediaproxy.SignURL` と byte 一致を parity test で担保)。なおカスタム絵文字と連合先インスタンスのアイコン/ファビコンは、本家フロント (`MkCustomEmoji`/`MkInstanceTicker`) が `meta.mediaProxy` で client 側プロキシ済のため、二重プロキシと本家 API shape からの乖離を避けて raw のまま返す。注: RSS ウィジェット (`/api/fetch-rss`) の画像/エンクロージャは別途対応予定

- Fix: チャットの `/api/chat/messages/room-timeline`・`/api/chat/messages`・`/api/chat/rooms/members` が、リクエスト元がルームのメンバーかどうかを検証せず、認証済みなら誰でも任意のルームの発言やメンバー一覧を読めた問題を修正 (本家同様 room-timeline/messages はメンバーまたはモデレーター、members はメンバーのみに限定し、それ以外およびルーム不在は `NO_SUCH_ROOM` を返す)

- Fix: チャットの `/api/chat/rooms/join` が招待なしで誰でも任意のルームに参加できた問題を修正 (本家同様 pending invitation を必須とし、参加成功時に招待を消費する)。`/api/chat/rooms/invitations/create` が自分自身/既存メンバー/招待済み/満室 (50人) のチェックを行わず、レスポンスも 204 で本家の `ChatRoomInvitation` オブジェクト (`id`/`createdAt`/`userId`/`user`/`roomId`/`room`) を返していなかった問題を修正。`/api/chat/rooms/joining` が `ChatRoom[]` を返していたのを本家同様 `ChatRoomMembership[]` (room 付き) に修正。あわせて join/invitation で `MAX_ROOM_MEMBERS=50` の満室ガードを追加。注: 招待時の `chatRoomInvitationReceived` 通知は mk-go の通知基盤が未対応のため後続対応

- Fix: `/api/admin/relays/add` が inbox URL のプロトコルを検証せず、`http://` や非 URL でもそのまま relay 行を作成しようとしていた問題を修正 (本家同様 https 以外 / parse 失敗を `INVALID_URL` で弾く)。あわせて `/api/admin/federation/update-instance`・`/api/admin/federation/refresh-remote-instance-metadata` が指定ホストの instance 行が存在しない場合に無言で 204 を返していた問題を修正 (本家同様 instance not found を 500 で伝播)。両エンドポイントで lookup 前にホストを toPuny 正規化 (punycode + 小文字化) するようにし、大文字混じり / IDN ホストでも instance を引けるように (本家 `utilityService.toPuny` 相当)。注: 取り込み側 (ActivityPub actor 解決) のホスト正規化は別スコープのため、生 Unicode で保存された IDN ホストとの整合は今後の課題

- Fix: `/api/admin/drive/clean-remote-files` の削除対象条件が逆 (`isLink=true` のリンク専用プロキシを消し、本来消すべき `isLink=false` のキャッシュ実体を残していた) で、かつオブジェクトストレージの物理ファイルを消していなかった問題を修正 (本家同様 `userHost IS NOT NULL AND isLink=false` を対象に、access/thumbnail/webpublic オブジェクトを削除してから DB 行を削除)。`/api/admin/delete-all-files-of-a-user` も DB 行のみ削除で物理ファイルが orphan 化していた問題を修正 (storage バックエンド配線時はオブジェクトも削除)

- Fix: `/api/admin/queue/stats` のレスポンスが `{deliver, inbox}` の2キーのみで本家の `db`/`objectStorage` キーを欠き、各値も `completed`/`failed` を含んでいなかった問題を修正 (本家同様 4キー + QueueCount `{waiting,active,completed,failed,delayed}`)。`/api/admin/queue/{remove-job,retry-job,show-job,show-job-logs}` が本家の `jobId` パラメータを受け付けず `id` しか読まなかった問題 (`jobId` を優先、`id` も後方互換) と、`/api/admin/queue/clear` が `state` パラメータを無視して常に pending のみ削除していた問題 (本家同様 state 別に削除、`*` で全 state) も修正。あわせて `queues`/`queue-stats` の `metrics.completed`/`failed` に必須の `meta` オブジェクトが欠けていた問題と、`/api/admin/queue/jobs` の `search` パラメータが無視されていた問題 (本家同様 JSON 表現への全 term マッチで最大100件) を修正

- Fix: `/api/admin/server-info` の `machine` がオブジェクト (`{name}`) で本家の文字列 (ホスト名) と型が異なり、`os`/`node`/`psql`/`redis`/`net.interface` が欠落していた問題を修正 (本家同様 machine は文字列、`os`(プラットフォーム)/`node`(ランタイム版)/`psql`(PostgreSQL版)/`redis`(Redis版)/`net.interface` を返す)。あわせて本家には無い `enableServerMachineStats` ゲートを admin から外し常に実値を返すように。公開 `/api/server-info` は本家同様 `machine`/`cpu`/`mem`/`fs` のみに限定し、`os`/`node`/`net.interface` 等が未認証クライアントに露出していた問題 (機械統計有効時) を修正

- Fix: `/api/admin/captcha/current` のレスポンスが flat な `{provider, siteKey}` で、本家の `{provider, hcaptcha:{...}, mcaptcha:{...}, recaptcha:{...}, turnstile:{...}}` という provider 別 nested 構造を欠いていた問題を修正 (フロントエンドが `res.hcaptcha.siteKey` 等を読めず undefined になっていた)。`/api/admin/captcha/save` が本家の generic パラメータ (`provider`/`captchaResult`/`sitekey`/`secret`/`instanceUrl`) を受け取らず保存値が DB に書かれていなかった問題、mCaptcha/testCaptcha provider 非対応、`captchaResult` の検証ステップと各エラーコード (`INVALID_PROVIDER`/`INVALID_PARAMETERS`/`VERIFICATION_FAILED` 等) の欠落も修正 (本家同様 captchaResult を検証し pass 時のみ設定を保存)。あわせて mCaptcha インスタンスの非200応答が `VERIFICATION_FAILED` になっていた問題を `REQUEST_FAILED` に修正 (本家 verifyMcaptcha と一致)

- Fix: `/api/admin/abuse-user-reports` の `reporter`/`targetUser`/`assignee` が生のユーザーモデルで返り、`usernameLower` 等の内部フィールドが漏れ UserDetailedNotMe 固有フィールドを欠いていた問題を修正 (本家同様 UserDetailed で pack、`assignee` は未割当でも `null` でキー常在)。あわせて `targetUserHost`/`reporterHost` の余剰フィールド露出も除去
- Fix: `/api/admin/update-abuse-user-report` が `moderationNote` を無視し代わりに `resolved=true` を立てていた問題 (resolve-abuse-user-report との取り違え) を修正 (本家同様 `moderationNote` のみ更新し、変化時に `updateAbuseReportNote` を記録)。`/api/admin/resolve-abuse-user-report` が `assigneeId` を記録していなかった問題も修正
- Fix: `/api/admin/forward-abuse-user-report` がローカル対象 (targetUserHost が null) や既に転送済みの通報をガードせず転送扱いにしていた問題と、存在しない通報で 404 `NO_SUCH_ABUSE_REPORT` を返していなかった問題を修正。あわせて abuse-report 系エンドポイントのエラーコードを本家の `NO_SUCH_ABUSE_REPORT` (エンドポイント別 UUID) に統一
- Fix: `/api/users/report-abuse` が対象ユーザーの存在・自己通報・管理者通報を検証しておらず (`NO_SUCH_USER`/`CANNOT_REPORT_YOURSELF`/`CANNOT_REPORT_THE_ADMIN`)、`comment` の最大長 (2048文字) 検証と `targetUserHost` の保存も欠いていた問題を修正
- Fix: `/api/admin/abuse-report/notification-recipient/*` のレスポンスが解決済みの `user` (UserLite) / `systemWebhook` (SystemWebhook) を含まず、`create`/`update` が `method=email` 時に対象ユーザーのメール検証 (`EMAIL_ADDRESS_NOT_SET`) を行わず、`method` に対応しない側 (`userId`/`systemWebhookId`) を null 化していなかった問題を修正 (本家 AbuseReportNotificationRecipientEntityService.pack と create/update.ts 相当)。あわせて `list` が本家の `method[]` (enum `email`/`webhook`) フィルタ・昇順ソートを欠き、enum 外の `method` 値を `create`/`update` と異なり 400 でなく 200+空配列で返していた問題、`update` の `method` 省略 partial-update 経由でメール検証を迂回して未 verify ユーザーをメール通知先に向けられた問題、`updatedAt` が本家 `toISOString()` と異なり秒ちょうどで `.000` を欠く形式で返っていた問題も修正
- Fix: `/api/admin/show-user` が `isHibernated`/`notificationRecieveConfig`/`moderationNote`/`followedMessage` を固定値で返し実データを欠いていた問題を修正 (プロフィール・ユーザーから取得)。あわせて非管理者 (モデレーター) が他の管理者の情報を閲覧できてしまう問題を修正 (本家同様 `cannot show info of admin` で拒否)
- Fix: `/api/admin/show-users` がモデレーター向け一覧で管理者専用フィールド (`email`/`signins`/`roleAssigns`/`notificationRecieveConfig`) を露出しており、`show-user` のアクセス制御を一覧経由で迂回できた問題を修正 (本家同様 UserDetailed shape で返す)。あわせて `sort` の並び順が本家と全方向逆だった問題 (`+createdAt` が昇順になっていた等) と、follower ソートのキー名 (`+followersCount`→`+follower`) の不一致、`username` プレフィックス絞り込みパラメータの未実装を修正。注: `sort` 未指定/不明値時の既定順が `id` 降順から本家同様の昇順 (古い順) に変わる (共通実装のため公開 `/api/users` にも影響)
- Fix: `/api/admin/meta` が DB に列があるにもかかわらず多数の設定 field (`uri`/`singleUserMode`/`allowExternalApRedirect`/`ugcVisibilityForVisitor`/各種画像URL・テーマ/`deeplAuthKey`/`translatorAvailable`/`notesPerOneAd`/`clientOptions`/`manifestJsonOverride`/`deliverSuspendedSoftware`/`urlPreview*`/`per*TimelineCacheMax`/`remoteNotesCleaning*` ほか) を固定値で返しており、管理画面が保存済み設定でなくデフォルトを表示し保存→再読込で巻き戻る問題を修正 (本家同様すべて `meta` から読む。`uri` はインスタンス URL を返す)。あわせて `/api/admin/meta` と公開 `/api/meta` の `policies` が `DEFAULT_POLICIES` と instance の override をマージせず返していた問題 (admin が変更した policy が反映されない) も修正
- Fix: `/api/admin/update-meta` で `mcaptchaSiteKey` が DB 列名と一致せず mCaptcha のサイトキーを保存できなかった問題を修正。あわせて本家同様の入力正規化を追加 (`blockedHosts`/`federationHosts` の小文字化、`silencedHosts`/`mediaSilencedHosts` の sort/重複除去/blockedHosts 除外、`langs` 等の空要素除去、`deeplAuthKey` 等の空文字→null 変換、`repositoryUrl` の URL 検証、`urlPreviewUserAgent`/`summalyProxy` の trim 処理)
- Fix: 下書き (`/api/notes/drafts/*`) のレスポンスが `text`/`cw`/`visibility`/`localOnly`/`fileIds` しか持たず、本家 NoteDraft schema の `userId`/`reactionAcceptance`/`visibleUserIds`/`replyId`/`renoteId`/`channelId`/`hashtag`/`poll`/`scheduledAt`/`isActuallyScheduled`/`user`/`files` を欠いていた問題を修正 (一覧では全下書きの `files` を 1 クエリにまとめて N+1 を回避)。あわせて `/api/notes/drafts/create`・`/update` が `visibleUserIds`/`reactionAcceptance`/`replyId`/`renoteId`/`channelId`/`hashtag`/`poll` を受け付けず、レスポンスを本家同様の `{createdDraft}`/`{updatedDraft}` で包んでいなかった問題と、`fileIds` の所有検証 (`NO_SUCH_FILE`) と期限切れ poll の拒否 (`CANNOT_CREATE_ALREADY_EXPIRED_POLL`) が無かった問題も修正
- Fix: ギャラリー投稿 (`/api/gallery/posts/*` と featured/popular/posts) のレスポンスで `files` が常に空配列で、閲覧者視点の `isLiked` も欠落していた問題を修正 (`fileIds` から DriveFile を解決。一覧では 1 クエリにまとめて N+1 を回避)。`/api/gallery/posts/create` が `fileIds` 必須・所有ファイル検証 (1-32 件 / 重複除去) を行わず、`/api/gallery/posts/update` が `fileIds`/`isSensitive` を無視して 204 を返していた問題も修正 (本家同様、所有ファイルのみ採用し更新後の GalleryPost を返す)

- Fix: ユーザーの `onlineStatus` が常に `unknown` 固定だった問題を修正 (`lastActiveDate` と `hideOnlineStatus` から online/active/offline を算出。本家同様 10分=online / 3日=active のしきい値)。タイムライン・通知など全 UserLite 応答に反映される
- Fix: `isSilenced` (サイレンス状態) が `/api/i` 以外のユーザー応答 (users/show・admin/show-user 等) で常に `false` 固定だった問題を修正 (role policy の `canPublicNote` を否定する role を持つかで算出。本家 UserEntityService 互換)

- Fix: `/api/channels/create`・`/update` が `bannerId` を無視し、`/update` が `pinnedNoteIds` を反映できなかった問題を修正 (bannerId は所有権検証付き、`""` で banner 解除)。`/api/channels/search` が `type` (`nameAndDescription` 既定 / `nameOnly`) を無視して description を検索対象にしていなかった問題と、archived チャンネルを検索結果から除外していなかった問題も修正。チャンネルの packed レスポンス (show / search / featured / owned 等) に閲覧者視点の `isMuting` が欠落していた問題を修正

- Fix: `/api/admin/roles/create`・`/show`・`/list` と公開 `/api/roles/list`・`/show` の Role レスポンスが raw な内部モデルで `usersCount`・`createdAt`・`target`・`condFormula`・`policies`(デフォルト値マージ)・`preserveAssignmentOnMoveAccount` を欠いていた問題を修正 (Misskey 本家 RoleEntityService.pack 相当の共通 packer に統一)。公開 `/api/roles/list` の `isExplorable` フィルタ欠落も合わせて修正 (本家同様 isPublic かつ isExplorable のみ列挙)

- Fix: `/api/admin/emoji/add` が `category`/`aliases`/`license`/`isSensitive`/`localOnly`/`roleIdsThatCanBeUsedThisEmojiAsReaction` パラメータを無視し、さらに raw な内部モデルを返して `url` 欠落 + `originalUrl`/`publicUrl`/`uri`/`type` 等の内部フィールドが漏れていた問題を修正 (Misskey 本家同様に全パラメータを永続化し EmojiDetailed を返す)。あわせて重複名 (DUPLICATE_NAME) / emoji 名パターン (`^[a-zA-Z0-9_]+$`) の検証を追加し、`fileId` 経路では本家 `FILE_TYPE_IMAGE` 許可リストで MIME を検証 (XSS 対策で `image/svg+xml` は除外) して `originalUrl`/`publicUrl`/`type` を webpublic variant 優先で永続化する。なお legacy な `url` 直接指定経路では MIME 検証は行わない (管理者専用)
- Fix: `/api/admin/emoji/update` が `fileId` による画像差し替え (webpublic variant 優先 + MIME 許可リスト検証) と `roleIdsThatCanBeUsedThisEmojiAsReaction` の更新を受け付けていなかった問題を修正 (リネーム時の SAME_NAME_EMOJI_EXISTS チェックも追加)
- Fix: `/api/emoji` (EmojiDetailed) のレスポンスに `license` と `roleIdsThatCanBeUsedThisEmojiAsReaction` が欠落し、`url` が `publicUrl` 固定で `originalUrl` フォールバックが無かった問題を修正
- Fix: `/api/admin/avatar-decorations/create`・`/list` のレスポンスに `createdAt` (aidx ID 由来) が含まれていなかった問題を修正

- Fix: 管理画面のプロモーション登録 (`/api/admin/promo/create`) で、公開範囲が public 以外のノート (フォロワー限定 / ホーム / specified) も登録できていた問題を修正 (将来 promo 表示エンドポイントが実装された際に非公開ノートが全閲覧者に漏れる潜在的問題を作成段階で防止)
- Fix: ユーザーリストのタイムライン (`/api/notes/user-list-timeline` の REST 経路は #1442 で修正済) について、WebSocket `userList` チャネルと fanout 配信が他ユーザーのフォロワー限定ノートをリスト所有者のフォロー関係に関係なくプッシュしていた問題を修正 (任意のユーザーをリストに追加しただけで、フォローしていない相手のフォロワー限定ノートをリアルタイムに受信できていた)
- Fix: アンテナが他ユーザーのフォロワー限定 / specified ノートを公開範囲チェックを経ずに pickup し、`/api/antennas/notes` と WebSocket `antenna` チャネルの両方からそれらが取得できる問題を修正 (アンテナ作成だけで `src=all` / `src=users` 経由のキーワード検索で非公開投稿が漏れていた)
- Fix: `useObjectStorage = true` のインスタンスで、`storedInternal = true` なドライブファイルへの `/files/:accessKey` アクセスが常に 404 になる問題を修正 (Misskey TS からの S3 移行前に保存されたローカルファイルが、移行後もアクセス可能に)
- Fix: Misskey TS から mk-go に移行した直後、既に期限切れだったアンケートに対してアンケート終了 (pollEnded) 通知が一斉発火する問題を修正 (移行時 backfill migration を追加)
- Fix: 配送先が一時的にダウンしている場合に、deliver/inbox ジョブが設定省略時にリトライされない問題を修正 (既定の試行回数を Misskey 本家と同じ deliver=12 / inbox=8 に。従来の no-retry 挙動が必要な場合は `deliverJobMaxAttempts` / `inboxJobMaxAttempts` に 1 を指定)
- Fix: `/api/i/notifications` の通知 payload に埋め込まれるノート本体が公開範囲チェックを経ずに返り、フォロワー限定ノートがリプライ通知などを介して非フォロワーに漏れる問題を修正
- Fix: `/api/drive/files/move-bulk` が呼び出しユーザーの所有権を検証せず、任意の `fileIds` を指定することで他人のドライブファイルをフォルダ移動できた問題 (IDOR) を修正 (更新を `userId` で絞り込み、宛先フォルダの所有権も検証)
- Fix: ActivityPub inbox で HTTP 署名者と activity の `actor` の一致を検証しておらず、有効な鍵で署名しつつ body の `actor` に他人を詐称した活動 (Delete / Create 等) を受理してしまう問題 (なりすまし) を修正 (署名者と actor が一致しない場合は、LD-Signature が actor を認証している転送活動のみ許可し、それ以外は破棄)
- Fix: `/api/chat/messages/create-to-user` で受信者が送信者をブロックしていても DM が届く問題を修正 (`YOU_HAVE_BEEN_BLOCKED` で拒否)
- Fix: 公開エンドポイントである `/api/federation/instances`・`/api/federation/show-instance`・`/api/federation/stats` がモデレーター専用フィールド `moderationNote` を誰にでも返していた問題を修正 (モデレーターに対してのみ実値を返し、それ以外は null)
- Fix: `/api/flash/featured`・`/api/flash/search` が公開範囲フィルタを欠き、非公開 (visibility != public) の Play が含まれていた問題を修正 (公開 Play のみを対象に。featured は likedCount > 0 条件も付与)
- Fix: `/api/admin/suspend-user` がモデレーター / 管理者 / root アカウントを凍結できてしまう問題を修正 (本家同様に拒否)。あわせて `/api/admin/delete-account`・`/api/admin/accounts/delete` で root アカウントの削除を防止

## 0.9.2

- Feat: グループチャットの連合に対応 (招待の送受信と Accept / Reject、ルームメッセージの送受信、退出の同期)
- Feat: リモートアカウントの自己削除 (Delete) を処理し、tombstone 化とカスケード削除を行うように
- Feat: `/api/fetch-external-resources`・`/api/drive/files/upload-from-url`・`/api/notifications/create`・カスタム絵文字の zip エクスポート・`/api/hashtags/users` を実装
- Feat: `/miauth/:session/check` に対応し、サードパーティクライアントからのログインを可能に
- Feat: 管理画面のジョブキューダッシュボードを整備 (キューごとのメモリ / 接続数 / 稼働時間、completed / failed ジョブの一覧と作成・処理時刻、mk-go が扱わないキューの非表示、ユーザー詳細のサインイン履歴 / ロール割り当て表示)
- Feat: Misskey 本家と mk-go の API 互換性マトリクスを自動生成するツールを追加
- Enhance: API 応答の shape drift を CI で検出するゲートを導入し、各 API のレスポンス形状を Misskey 本家に整合
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

## 互換バージョン: Misskey 2026.5.4 (#1164 で submodule bump、2026.5.1-mk.0 → 2026.5.4-mk.0)

### Phase 18 — 2026.5.4 upstream 追従 + LD-Signature 初期実装 (#1164 完了)

submodule を 2026.5.1-mk.0 → 2026.5.4-mk.0 に bump、その間に発生した drift を移植 + 長年の drop-in gap だった LD-Signature 検証の初期実装を 1 PR に集約 (旧 #1118 統合):

- **Phase A** (2026.5.2 由来):
  - `/api/following/list` を新設 (upstream #17385 + #17416)。kind read:following、notification=true で notify IS NOT NULL filter、cursor sinceId/untilId + limit 1-100 default 10。FollowingEntityService.pack 互換の Following entity を `internal/entity/following.go` に追加
  - `/api/following/update` の notify="none" を SQL NULL に正規化 (#17385 完全対応)。これがないと following/list の notification=true filter が機能不全だった drop-in regression を解消
  - twemoji 参照 path を `node_modules/@misskey-dev/emoji-assets/built/twemoji` に追従 (upstream #17381)
- **Phase B**: submodule bump 2026.5.1-mk.0 → 2026.5.4-mk.0
- **Phase C** (2026.5.4 由来):
  - `chat/rooms/show` に HasPermissionToViewRoomInfo gate を追加。owner / member / 招待受信者 / moderator のみ閲覧可、それ以外は noSuchRoom 404。旧 mk-go の RoomsShow は権限 check 完全欠如で room id を知る任意 user が room メタを取得できる drop-in regression / privacy hole だった
  - `/api/announcements/show` の userId-targeted gate を実装。非ログイン user / 他人宛 announcement へのアクセスを 404 で塞ぐ (upstream 2026.5.4 fix + mk-go 単体の旧穴 2 重対応)
- **Phase D** (LD-Signature 初期実装 + 2026.5.4 hardening):
  - `internal/activitypub/ld/` を新設。`piprate/json-gold` v0.8.0 + URDNA2015 canonicalize の土台
  - Misskey TS `PRELOADED_CONTEXTS` 互換の 3 context document (AS 2.0 / Security v1 / Identity v1) を JSON file として embed、PreloadedLoader が HTTP fetch なしで context resolve
  - RsaSignature2017 verify を実装 (= upstream JsonLdService.verifyRsaSignature2017 byte-for-byte 互換、createVerifyData → SHA-256 hex concat → RSA-PKCS1v15 SHA-256 verify)
  - 2026.5.4 hardening を初期実装に組み込む: forbidden directives (`@included` / `@graph` / `@reverse` を含む activity を弾く) / per-call cache 上限 256 entry / Freeze() で SSRF + TOCTOU race 遮断
  - InboxProcessor 経路で全 inbound activity に対する LD-Sig optional verify を追加。signature 無し → skip / verify pass → 通常処理 / verify fail → drop。relay 由来 Announce 挙動 (#1002) は upstream 互換維持 (= LD-Sig pass しても renote 生成せず stream publish のみ)
- 全 11 commit、`make fmt && make lint && make test` 全 pass、federation カバレッジ 90% 以上維持

旧 #1118 (2026.5.1 → 2026.5.3 追従 tracker) は本 PR に統合して close。`docs/update/20260502diff.md` / `docs/update/20260504diff.md` の「mk-go 対応」サマリを「移植完了」に update した。

### Phase 17 — Ed25519 サポート (FEP-521a Multikey, #1067 完了)

mk-go 独自の先行実装として、HTTP Signature の Ed25519 対応を 6 phase で導入。Fedibird (Mastodon フォーク) など FEP-521a 対応サーバーが Ed25519 鍵を expose する流れに先んじて、upstream Misskey TS が未対応の Ed25519 を mk-go では capability-gated で sign/verify する。

- P1 (#1068 / PR #1074): schema migration + 鍵生成 配線。`user_keypair_extra` / `user_publickey_extra` add-only table、signup / systemaccount で RSA と並行発行
- P2 (#1069 / PR #1078): Renderer で `assertionMethod[]` expose + Multikey encode helper (`github.com/multiformats/go-multibase` 利用)
- P3 (#1070 / PR #1079): Resolver で remote actor の `assertionMethod[]` parse → `user_publickey_extra` upsert + keyId dual lookup + stale key 自動削除 (key rotation 対応 / security fix)
- P4 (#1071 / PR #1080): Outbound deliver で capability-gated Ed25519 sign + Redis INCR ベースの 4xx degrade safeguard (閾値 3 / 60s window) + 5min cache + singleflight
- P5 (#1072 / PR #1081): 既存 local user の lazy backfill (= TS で signup された旧 user に対しても actor JSON 初回 fetch で Ed25519 鍵を自動発行) + singleflight 集約
- P6 (#1073 / PR #1084): drop-in test に MUST シナリオ (mk-A 切替後の actor JSON で Ed25519 expose / 再 fetch で安定) を追加
- P6 follow-up SHOULD (#1082 / PR #1085): mk-A → TS-A 戻し時の連合継続を drop-in test (`run-swap-test.sh` stage 7-9) で検証
- P6 follow-up NICE-TO-HAVE (#1083 / PR #1086): Python ベースの Fedibird-like ActivityPub mock (`tests/dropin/fedibird_mock/`) と `run-fedibird-test.sh` orchestrator + `test_fedibird_ed25519.py` 3 経路 (actor fetch / mock→mk-A inbox Ed25519 sign / mk-A→mock outbound Ed25519 sign) を追加

drop-in 互換: 既存 `user_keypair` / `user_publickey` テーブル untouched、Misskey TS は新規 extra テーブル無視 → TS 戻し時の連合継続。`PASSWORD_TOO_LONG` 以外の error code drift も発生せず。

### Phase 16 — Playwright e2e Phase 1-4 (#744 完了)

両 backend (mk-go / Misskey TS) 並列実行で drop-in 互換 regression を nightly 監視する基盤を 4 phase で整備:

- Phase 1 (#795-#840): 基盤 + auth / notes / drive / streaming / 2FA TOTP / 2FA Passkey spec
- Phase 2 (#842-#901): timeline / users / reactions / chat / notification / emoji / pages / gallery / clips / flash / reversi / settings / search / drive 拡張 / admin / role / meta
- Phase 3 (#902-#922): charts / federation / ap / sw / antennas / invite / auth-app / miauth / announcements / utility / i 拡張 / bubble-game / reversi multiplayer
- Phase 4 (#923-#934): channels / hashtags / roles / notifications control / admin/queue / admin/abuse-report / admin/{stats,show,ad,avatar-decorations,drive read} / admin/{server-info,captcha,invite,announcements,relays,system-webhook} / i webhooks / chat extra

達成: **96 spec / 35 directory / 242 endpoint cover (54.3%)**、両 backend で nightly green。

### Phase P5 — Drift backlog (Playwright で発見した 40+ 件)

Playwright spec を両 backend で走らせる中で観測した drop-in 互換 drift を順次 fix:

#### auth / signup / signin
- #798: signup duplicate username の status code (400 DUPLICATED_USERNAME)
- #800: username 長さ制限 (20 char max)
- #802: error response body shape (Misskey misc 形式)

#### note / timeline
- #799: notes/show で visibility 違反時の挙動 (200 で stub note)
- #874 + perf #892 / #894: timeline endpoint で user mute filter 追加 + SQL push-down + muting subquery 化
- #876: users/lists/list の N+1 query を batch fetch に
- #877: notes/search の `fulltextSearch.provider: "none"` opt-in (= upstream TS strict-mode 互換 400 UNAVAILABLE) を追加。mk-go 既定は引き続き `sqlLike` fallback で動く
- #878: users/search-by-username-and-host suspend filter

#### drive
- #812: drive/files/create userId / user / folder shape
- #818: drive/files/find / find-by-hash の packMany self path
- #845: drive/folders/show detail mode (parent / counts)
- #977: drive/folders 系の `NO_SUCH_FOLDER` UUID を endpoint 別 (create `53326628-...` / show `d74ab9eb-...` / update `f7974dac-...` / delete `1069098f-...`) に分割。`folders/update` の parent 不在を `NO_SUCH_PARENT_FOLDER` (`ce104e3a-...`) として区別するため `ErrParentFolderNotFound` を追加

#### chat
- #851 / #855 / #860: chat packMessage / packRoom の null 省略 / field set drift
- #864: reaction emoji の variation selector 正規化

#### users / following / blocking
- #870: blocking/create / delete return shape (UserDetailed 返却)
- #871: users/lists/create response shape (createdAt / userIds / isPublic)
- #872: blocked → following/create reject status (400)
- #984: users/relation を stub から実装に切替。viewer ↔ target の follow / follow-request / block / mute / renote-mute 状態を 5 repo (`following` / `follow_request` / `blocking` / `muting` / `renote_muting`) から実 DB 状態として返す
- #970: `/api/users/show` で viewer===target のとき MeDetailed 拡張 field (isExplorable / noCrawle / emailNotificationTypes 等 14 個) を merge して返すよう拡張。upstream `pack(user, me)` semantics と一致させ、`/api/users/show?username=me` 経由でも `/api/i` と同じ shape を保つ。新規 helper `entity.AsMeDetailed` で pre-built UserDetailed を promote する design
- #988: `canChat` 二重 drift 解消。`PackUserLite` が \`u.ChatScope != "none"\` でなく **role policy の `chatAvailability === "available"`** から derive するように変更 (upstream `UserEntityService.ts:561` 互換)。新規 `entity.CanChatLookup` interface を `internal/entity/can_chat.go` に追加し、`role.CanChatLookupAdapter` で role.Service を bridge する design (= `entity.SetAvatarDecorationLookup` と同 pattern)。`/api/i` Me handler の `resp["canChat"] = true` hardcode も撤去し、self-view でも role policy が反映される
- splash 画面 drift 解消: SPA ロード中の `<div id="splash">` markup を upstream `_splash.tsx` 互換に揃える (#993)。旧実装は mascotImageUrl (= ai.png) を中央に表示し "Loading..." text だけ出していたが、upstream は server iconUrl + 回転 spinner SVG (bg/fg 2 枚) を表示する。修正後: `<img id="splashIcon" src="...">` + `<div id="splashSpinner">` (2 SVG)。default icon は `/static-assets/splash.png` (Misskey ロゴ)、admin が meta.iconUrl を設定していればそれを使う

#### settings / token
- #883: i/regenerate-token return shape (204)
- #884: 旧 API token を cache 経由でも即時 reject
- #885: i/update-email error status 標準化
- #910: app-issued access token を auth middleware で dual lookup (raw → hash)
- #913: i/revoke-token も dual lookup + cache invalidation
- #985: `entity.PackMeDetailed` に `emailNotificationTypes` / `mutingNotificationTypes` / `notificationRecieveConfig` を追加。i/update 経路でも 3 field が返るようになり、frontend の `updateCurrentAccountPartial` が settings/email 等の toggle 後に local state を正しく反映できる。値は `user_profile` の JSON column を unmarshal して取得し、parse 失敗時は upstream default (`["follow","receiveFollowRequest"]` / `[]` / `{}`) に倒す
- #971: `/api/i` Me handler を `PackMeDetailed` ベースに refactor。JSON round-trip で MeDetailed を resp map base に展開し、固有 field (email / mutedWords / twoFactor / clientData / role / unread 等) を merge する design。重複コード 25 行削減、MeDetailed packer 更新時に自動追従

#### admin
- #888: admin/show-user shape (roles / policies / signins / roleAssigns / isHibernated / lastActiveDate)
- #889: admin/roles/create paramDef strict (13 field required)
- #896 / #900 / #901 / #931 / #932: pq.StringArray drift (avatar-decorations / system-webhook / flash / antenna / emoji bulk の Updates(map) で空 string[] が NULL 化される問題を `pq.StringArray()` ラップで解消)
- #907: i/registry/set で domain field の handling
- #915: federation/show-instance を 204 No Content に
- #918: sw/show-registration を 204 No Content に
- #925: hashtags/* paramDef strictness drift
- #926: channels の Playwright LCD strict 化
- #929: admin/queue + admin/abuse-report の paramDef strictness / idempotency
- #936: i/webhooks/update を 204 No Content に
- #937: i/webhooks/test の paramDef (type required + webhookEventTypes enum)
- #939: i/webhooks/{create,update} の on array enum check

#### UDS production 由来
- #940: 遅延配送 remote note の createdAt drift (AP `published` を採用 + clock skew/floor guard)
- #941: カスタム絵文字 (リアクション / picker) のアニメ pass-through (mediaproxy で gif/apng を resize しない)
- #942: URL summary 文字化け解消 (Shift_JIS / EUC-JP / ISO-2022-JP の自動正規化)
- #943: リモートユーザー counts (notesCount / followersCount / followingCount) を origin から fetch する mk-go 独自拡張
- #945: RemoteStatsFetcher cache を `sync.Map` → LRU (size cap 10000) で memory bound 化
- #1156: リモートユーザーのプロフィール「アクティビティ」タブが Heatmap 空 / Notes 折れ線がマイナス方面にのみ動く drop-in regression を fix。federation `handleCreate` / `handleAnnounce` で `OnNoteCreated` chart hook が発火しておらず PerUserNotesChart の +1 が記録されない一方、`handleDelete` 経路 (`NoteDeleteService.Delete`) は -1 を記録していたため日次集計が常に負方向に推移していた。`Resolver.IngestNoteWithCreated` (sibling) を追加して dedup-hit と新規 INSERT を区別し、`Processor.SetNoteChartHook` で chart hook を `safeGoFedHook` 経由で fire-and-forget 発火 (重複配送では fire しない)
- #1167: fluent-emoji 参照 path を `@misskey-dev/emoji-assets/built/fluent-emoji` に追従 (upstream 2026.5.2 #17381 残作業、Phase 18 #1164 commit 3 の twemoji 追従と同時にやるべきだった漏れ補完)。修正前は旧 path (submodule `fluent-emojis/dist`) が残っていたため mk-go だけ achievement 1/2/3 icon が表示できていた drop-in 違反状態。修正後は upstream Misskey TS 2026.5.4 と同じ broken state (= `passedSinceAccountCreated1/2/3` の bronze/silver/gold バッジ icon が 404) に揃え、frontend `achievements.ts` 側で keycap 系を新 path 存在 emoji に rename する upstream / fork fix を待つ運用に
- #1166: pagination の `sinceDate` / `untilDate` (Unix ms) を 20 file 42 handler 横断で実装 (= UI list 系 endpoint 主要 cover)。`internal/misc/id/NormalizeCursor` helper で sinceDate → `AidxCutoffPrefix(time)` の prefix に変換、handler 層で正規化してから repository に従来の (sinceID, untilID) を渡す adapter 設計で repository signature 不変。同時に既存 `notes/timeline_handler.go` / `notes/handler.go` の旧 ad-hoc 実装 (= `idGen.Generate(time)` で完全 ID を生成、ランダムな nodeID + counter suffix のため同 msec の早期 ID を取りこぼす bug) を `AidxCutoffPrefix` ベースに統一して deterministic + 全 ID 包含 cursor に修正。残り admin / federation / drive / mute / blocking / roles / etc. の 12 file は別 PR follow-up
- #1173: pagination 横断対応の follow-up。admin / utility 12 file 18 handler を #1166 と同 adapter pattern で対応 (admin/handler.go 4 = RolesUsers / EmojiList / EmojiListV2 / AbuseReports, admin/drive.go 1, admin/emoji.go 1, federation/handler.go 1, drive/handler.go 4, invite/handler.go 1, mute/handler.go 1, blocking/handler.go 1, renotemute/handler.go 1, reversi/handler.go 1, roles/handler.go 1, announcements/handler.go 2)。残り `notifications/handler.go` は sinceID 自体 repo 未配線の別 設計課題のため #1174 で扱う
- #1174: pagination 横断対応の最終 piece。`/api/i/notifications` の sinceDate / untilDate を adapter pattern で対応。当初 issue 想定では `sinceID` 自体 repo 未配線と認識していたが調査で実装は **post-fetch filter で aidx 比較** していると判明 (= silent ignore ではなく一応動いていた、line 131-134)。Redis Stream native ID と aidx ID は別物だが `Notification.ID` が `idGen.Generate()` で aidx 形式に発番されるため adapter pattern (= sinceDate → aidx prefix) で完結。これで **#1166 の handler-side cursor 対応は全 21 file 61 handler で完了**

### Phase 15 — Federation performance (#562, #565, #569)

- inbox handler を verify-in-worker 化 (#565): HTTP handler は body + signature header だけ payload に詰めて 202 即返し、signature verify / host block / instance touch / chart hook を inbox worker 側で実行。queue-bench で HTTP 受信 rps が **TS の 2.6-2.8x** 達成
- redundant `hydrateNoteForFanout` SELECT を skip (#569)
- `MarkRequestReceived` を per-host で 1s buffer に集約する `InstanceTouchBuffer` 導入
- fanoutHook / notificationHook を `safeGo` で async 化

### Phase 14 — Drop-in frontend e2e (#380, #381, #387, #394)

3 Misskey TS インスタンス + cypress 構成で frontend 視点の drop-in 互換を検証する基盤:

- Phase 14-1 (#381): 3 TS instance (A/B/C) + cypress runner + baseline smoke spec
- Phase 14-2 (#387): visibility / user_list / cross_instance_view / delete_note spec 拡充
- Phase 14-3 (#394): mk overlay (TS-A → mk-A 切替) で swap mode 動作確認、`CYPRESS_MODE=baseline|swap` で skip 制御
- nightly CI 19:00 UTC

### Phase 13 — Drop-in e2e (pytest, #365, #367, #372, #374)

Misskey TS 2 インスタンス + pytest で federation smoke + 状態継承を検証:

- Phase 13-1 (#365): TS-A / TS-B 2 stack + smoke
- Phase 13-2 (#367): mk-go overlay (TS-A backend を mk-A に差し替え) で state preservation 検証 + `migration/000039_dropin_compat.up.sql` で `note.pageCount` / `note.renoteChannelId` 補填
- Phase 13-3 (#372): state preservation 機能マトリクスを 6 シナリオに拡充
- Phase 13-4 (#374): nightly CI 18:00 UTC

### mkq driver + queue-bench (#571 audit, #563)

- `tests/queue-bench/` で BullMQ (TS) / asynq (mk-go) / mkq (mk-go) を 3-way 比較する基盤
- mkq driver を default 化 (asynq は legacy / future-deprecation candidate)
- 結果: mkq が deliver / inbox throughput とも最良

### docs/update — upstream 差分追跡 (#947, #948)

- `docs/update/20260500diff.md`: 2026.3.2 → 2026.5.0 backend diff (#947 で sub-task 化)
- `docs/update/20260501diff.md`: 2026.5.0 → 2026.5.1 backend diff
- 命名規則 `yyyymmdd*` で `docs/update/` に積み上げ運用

### Phase P3 — 補助エンドポイント + 欠損テーブル

- 欠損テーブル7種追加 (channel_favorite, clip_favorite, retention_aggregation等)
- 補助エンドポイント12+追加 (roles/notes, hashtags, gallery等)

### Phase P2 — 互換性修正 (#107サブissue)

- P2.1: パスワードリセット、MiAuth gen-token、App API、サインイン履歴
- P2.2: タイムラインフィルタリング、signin-flow captcha
- P2.3: NoteEntity/UserDetailedレスポンス完全化
- P2.4: AP MFM→HTML変換、attachment、context拡充

### Phase P1 — 第2次互換性修正 (#124サブissue)

- WebSocket 9チャンネル追加 (100%カバー)
- AP Inbox Block/Flag/Move/Add/Remove
- AP Accept完全実装 + Question(投票)受信
- users/lists update + update-membership
- trustProxyサポート
- エラーレスポンスUUID統一
- DBスキーマ欠損カラム追加
- WebSocketプロトコル改善 (OAuth2スコープ, pong応答)
- dbSlaves (リードレプリカ)サポート
- chart tables, social auth, AP拡張, Sentry

### Phase 11 — E2E + テストモード

- Cypress E2Eテスト基盤
- `/api/reset-db`テスト用エンドポイント

### Phase 10 — 管理機能

- admin/* 全エンドポイント実装

### Phase 9 — 認証 + ActivityPub

- 9.1: TOTP 2要素認証
- 9.2: リモートActivityPubオブジェクト解決

### Phase 1-8 — 基盤構築

- HTTPサーバー、DB/Redis接続、設定ローダー
- ユーザー、ノート、タイムライン、ドライブ
- フォロー、リアクション、通知
- WebSocketストリーミング
- ActivityPub送信/受信
- ジョブキュー (asynq)
- 検索 (Meilisearch/SQLフォールバック)
