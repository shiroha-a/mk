-- Phase: Misskey DB スキーマとの完全互換化 (issue #21)
-- 本家 Misskey (TypeScript) と mk-go の DB 差分を埋めるための一括 migration。
-- 以下 5 つのサブ変更を 1 ファイルにまとめている:
--   1. poll_vote.createdAt の除去 (mk-go 側の余剰カラム)
--   2. user_profile に 3 カラム追加 (twoFactorBackupSecret / clientData / room)
--   3. meta に 58 カラム追加 (branding / captcha / sensitiveMedia / urlPreview /
--      cacheTuning / deepL / stats / reactions / singleUserMode など)
--   4. 5 テーブル追加 (user_security_key / user_ip / user_memo / promo_note /
--      promo_read) — schema only、機能実装は後続 issue
--   5. TypeORM 互換 migrations テーブルを追加し 341 件の本家 migration を seed

-- =============================================================================
-- 1. poll_vote.createdAt の除去
-- =============================================================================
-- 本家 Misskey の PollVote エンティティには createdAt カラムが無い。mk-go
-- 側で書き込みのみ行っており読み取り参照はないため、安全に除去できる。
ALTER TABLE "poll_vote" DROP COLUMN IF EXISTS "createdAt";

-- =============================================================================
-- 2. user_profile に 3 カラム追加
-- =============================================================================
-- 本家 Misskey UserProfile に存在するが mk-go 側で未追加だったフィールド。
-- 機能実装 (2FA backup code / UI client data / chat room metadata) は後続
-- issue で行う。DB レベルでの互換性のみを確保する。
ALTER TABLE "user_profile"
    ADD COLUMN IF NOT EXISTS "twoFactorBackupSecret" varchar[],
    ADD COLUMN IF NOT EXISTS "clientData" jsonb NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS "room" jsonb NOT NULL DEFAULT '{}';

-- =============================================================================
-- 3. meta に 58 カラム追加
-- =============================================================================
-- 全て nullable または default 付きで追加。admin/update-meta エンドポイントは
-- generic map[string]any で書き込むので Go モデルさえ揃えれば API 互換。

-- --- branding (8) ---
ALTER TABLE "meta"
    ADD COLUMN IF NOT EXISTS "mascotImageUrl" varchar(1024),
    ADD COLUMN IF NOT EXISTS "app192IconUrl" varchar(1024),
    ADD COLUMN IF NOT EXISTS "app512IconUrl" varchar(1024),
    ADD COLUMN IF NOT EXISTS "serverErrorImageUrl" varchar(1024),
    ADD COLUMN IF NOT EXISTS "notFoundImageUrl" varchar(1024),
    ADD COLUMN IF NOT EXISTS "infoImageUrl" varchar(1024),
    ADD COLUMN IF NOT EXISTS "defaultLightTheme" varchar(8192),
    ADD COLUMN IF NOT EXISTS "defaultDarkTheme" varchar(8192);

-- --- CAPTCHA / email validation (11) ---
ALTER TABLE "meta"
    ADD COLUMN IF NOT EXISTS "enableMcaptcha" boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS "mcaptchaSitekey" varchar(1024),
    ADD COLUMN IF NOT EXISTS "mcaptchaSecretKey" varchar(1024),
    ADD COLUMN IF NOT EXISTS "mcaptchaInstanceUrl" varchar(1024),
    ADD COLUMN IF NOT EXISTS "enableTestcaptcha" boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS "enableActiveEmailValidation" boolean NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS "enableVerifymailApi" boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS "verifymailAuthKey" varchar(1024),
    ADD COLUMN IF NOT EXISTS "enableTruemailApi" boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS "truemailInstance" varchar(1024),
    ADD COLUMN IF NOT EXISTS "truemailAuthKey" varchar(1024);

-- --- sensitive media detection (5) ---
-- 本家は enum を使うが、Go 側で特定の enum 型を定義する必要を避けるため
-- varchar(128) にする。チェック制約は後続 issue で追加予定。
ALTER TABLE "meta"
    ADD COLUMN IF NOT EXISTS "sensitiveMediaDetection" varchar(128) NOT NULL DEFAULT 'none',
    ADD COLUMN IF NOT EXISTS "sensitiveMediaDetectionSensitivity" varchar(128) NOT NULL DEFAULT 'medium',
    ADD COLUMN IF NOT EXISTS "setSensitiveFlagAutomatically" boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS "enableSensitiveMediaDetectionForVideos" boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS "mediaSilencedHosts" varchar(1024)[] NOT NULL DEFAULT '{}';

-- --- URL preview (7) ---
ALTER TABLE "meta"
    ADD COLUMN IF NOT EXISTS "urlPreviewEnabled" boolean NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS "urlPreviewAllowRedirect" boolean NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS "urlPreviewTimeout" integer NOT NULL DEFAULT 10000,
    ADD COLUMN IF NOT EXISTS "urlPreviewMaximumContentLength" bigint NOT NULL DEFAULT 10485760,
    ADD COLUMN IF NOT EXISTS "urlPreviewRequireContentLength" boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS "urlPreviewSummaryProxyUrl" varchar(1024),
    ADD COLUMN IF NOT EXISTS "urlPreviewUserAgent" varchar(1024);

-- --- cache tuning (4) ---
ALTER TABLE "meta"
    ADD COLUMN IF NOT EXISTS "perLocalUserUserTimelineCacheMax" integer NOT NULL DEFAULT 300,
    ADD COLUMN IF NOT EXISTS "perRemoteUserUserTimelineCacheMax" integer NOT NULL DEFAULT 100,
    ADD COLUMN IF NOT EXISTS "perUserHomeTimelineCacheMax" integer NOT NULL DEFAULT 300,
    ADD COLUMN IF NOT EXISTS "perUserListTimelineCacheMax" integer NOT NULL DEFAULT 300;

-- --- ads / usernames / emails (5) ---
ALTER TABLE "meta"
    ADD COLUMN IF NOT EXISTS "notesPerOneAd" integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS "manifestJsonOverride" varchar(8192) NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS "bannedEmailDomains" varchar(1024)[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS "preservedUsernames" varchar(1024)[] NOT NULL DEFAULT
        ARRAY['admin', 'administrator', 'root', 'system', 'maintainer', 'host', 'mod',
              'moderator', 'owner', 'superuser', 'staff', 'auth', 'i', 'me', 'everyone',
              'all', 'mention', 'mentions', 'example', 'user', 'users', 'account',
              'accounts', 'official', 'help', 'helps', 'support', 'supports', 'info',
              'information', 'informations', 'announce', 'announces', 'announcement',
              'announcements', 'notice', 'notification', 'notifications', 'dev',
              'developer', 'developers', 'tech', 'misskey']::varchar(1024)[],
    ADD COLUMN IF NOT EXISTS "prohibitedWordsForNameOfUser" varchar(1024)[] NOT NULL DEFAULT '{}';

-- --- DeepL (2) ---
ALTER TABLE "meta"
    ADD COLUMN IF NOT EXISTS "deeplAuthKey" varchar(1024),
    ADD COLUMN IF NOT EXISTS "deeplIsPro" boolean NOT NULL DEFAULT false;

-- --- indexing / stats / machine (7) ---
ALTER TABLE "meta"
    ADD COLUMN IF NOT EXISTS "enableIdenticonGeneration" boolean NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS "enableIpLogging" boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS "enableChartsForRemoteUser" boolean NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS "enableChartsForFederatedInstances" boolean NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS "enableStatsForFederatedInstances" boolean NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS "enableServerMachineStats" boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS "showRoleBadgesOfRemoteUsers" boolean NOT NULL DEFAULT false;

-- --- reactions / cleaning (5) ---
ALTER TABLE "meta"
    ADD COLUMN IF NOT EXISTS "enableReactionsBuffering" boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS "allowExternalApRedirect" boolean NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS "enableRemoteNotesCleaning" boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS "remoteNotesCleaningMaxProcessingDurationInMinutes" integer NOT NULL DEFAULT 60,
    ADD COLUMN IF NOT EXISTS "remoteNotesCleaningExpiryDaysForEachNotes" integer NOT NULL DEFAULT 90;

-- --- system / instance options (7) ---
ALTER TABLE "meta"
    ADD COLUMN IF NOT EXISTS "singleUserMode" boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS "googleAnalyticsMeasurementId" varchar(64),
    ADD COLUMN IF NOT EXISTS "inquiryUrl" varchar(1024),
    ADD COLUMN IF NOT EXISTS "ugcVisibilityForVisitor" varchar(128) NOT NULL DEFAULT 'local',
    ADD COLUMN IF NOT EXISTS "clientOptions" jsonb NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS "deliverSuspendedSoftware" jsonb NOT NULL DEFAULT '[]';

-- --- 既存カラムのデフォルト値を本家に合わせる ---
-- 本家は repositoryUrl / feedbackUrl にハードコードデフォルトを持つ。新規
-- インストール時に本家と同じ挙動になるよう合わせておく。
ALTER TABLE "meta" ALTER COLUMN "repositoryUrl" SET DEFAULT 'https://github.com/misskey-dev/misskey';
ALTER TABLE "meta" ALTER COLUMN "feedbackUrl" SET DEFAULT 'https://github.com/misskey-dev/misskey/issues/new';

-- =============================================================================
-- 4. 欠落テーブル 5 つ (schema only)
-- =============================================================================

-- --- user_security_key (WebAuthn/FIDO2) ---
CREATE TABLE IF NOT EXISTS "user_security_key" (
    "id" varchar PRIMARY KEY,
    "userId" varchar(32) NOT NULL REFERENCES "user"("id") ON DELETE CASCADE,
    "name" varchar(30) NOT NULL,
    "publicKey" varchar NOT NULL,
    "counter" bigint NOT NULL DEFAULT 0,
    "lastUsed" timestamp with time zone NOT NULL DEFAULT now(),
    "credentialDeviceType" varchar(32),
    "credentialBackedUp" boolean,
    "transports" varchar(32)[]
);
CREATE INDEX IF NOT EXISTS "IDX_user_security_key_userId" ON "user_security_key" ("userId");
CREATE INDEX IF NOT EXISTS "IDX_user_security_key_publicKey" ON "user_security_key" ("publicKey");

-- --- user_ip ---
-- id は本家が PrimaryGeneratedColumn (SERIAL) を使うので bigserial で揃える。
CREATE TABLE IF NOT EXISTS "user_ip" (
    "id" bigserial PRIMARY KEY,
    "createdAt" timestamp with time zone NOT NULL,
    "userId" varchar(32) NOT NULL,
    "ip" varchar(128) NOT NULL
);
CREATE INDEX IF NOT EXISTS "IDX_user_ip_userId" ON "user_ip" ("userId");
CREATE UNIQUE INDEX IF NOT EXISTS "IDX_user_ip_userId_ip" ON "user_ip" ("userId", "ip");

-- --- user_memo ---
CREATE TABLE IF NOT EXISTS "user_memo" (
    "id" varchar(32) PRIMARY KEY,
    "userId" varchar(32) NOT NULL REFERENCES "user"("id") ON DELETE CASCADE,
    "targetUserId" varchar(32) NOT NULL REFERENCES "user"("id") ON DELETE CASCADE,
    "memo" varchar(2048) NOT NULL
);
CREATE INDEX IF NOT EXISTS "IDX_user_memo_userId" ON "user_memo" ("userId");
CREATE INDEX IF NOT EXISTS "IDX_user_memo_targetUserId" ON "user_memo" ("targetUserId");
CREATE UNIQUE INDEX IF NOT EXISTS "IDX_user_memo_userId_targetUserId" ON "user_memo" ("userId", "targetUserId");

-- --- promo_note ---
CREATE TABLE IF NOT EXISTS "promo_note" (
    "noteId" varchar(32) PRIMARY KEY REFERENCES "note"("id") ON DELETE CASCADE,
    "expiresAt" timestamp with time zone NOT NULL,
    "userId" varchar(32) NOT NULL
);
CREATE INDEX IF NOT EXISTS "IDX_promo_note_userId" ON "promo_note" ("userId");

-- --- promo_read ---
CREATE TABLE IF NOT EXISTS "promo_read" (
    "id" varchar(32) PRIMARY KEY,
    "userId" varchar(32) NOT NULL REFERENCES "user"("id") ON DELETE CASCADE,
    "noteId" varchar(32) NOT NULL REFERENCES "note"("id") ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS "IDX_promo_read_userId" ON "promo_read" ("userId");
CREATE UNIQUE INDEX IF NOT EXISTS "IDX_promo_read_userId_noteId" ON "promo_read" ("userId", "noteId");

-- =============================================================================
-- 5. TypeORM 互換 migrations テーブル
-- =============================================================================
-- 本家 Misskey (TypeORM) は起動時に "migrations" テーブルを参照して未実行の
-- migration を検出しようとする。mk-go は golang-migrate の
-- "schema_migrations" を使うので両者は共存できる。
--
-- 互換性を確保するため、本家で既知の全 migration エントリを seed することで、
-- mk-go で動かした DB を本家 Misskey に差し替えたときに「全て適用済み」
-- と認識させる。これにより DB migration 不要で両者を行き来できる。
CREATE TABLE IF NOT EXISTS "migrations" (
    "id" SERIAL PRIMARY KEY,
    "timestamp" bigint NOT NULL,
    "name" varchar NOT NULL
);

INSERT INTO "migrations" ("timestamp", "name")
SELECT v.ts, v.n FROM (VALUES
    (1000000000000::bigint, 'Init'),
    (1556348509290::bigint, 'Pages'),
    (1556746559567::bigint, 'UserProfile'),
    (1557476068003::bigint, 'PinnedUsers'),
    (1557761316509::bigint, 'AddSomeUrls'),
    (1557932705754::bigint, 'ObjectStorageSetting'),
    (1558072954435::bigint, 'PageLike'),
    (1558103093633::bigint, 'UserGroup'),
    (1558257926829::bigint, 'UserGroupInvite'),
    (1558266512381::bigint, 'UserListJoining'),
    (1561706992953::bigint, 'webauthn'),
    (1561873850023::bigint, 'ChartIndexes'),
    (1562422242907::bigint, 'PasswordLessLogin'),
    (1562444565093::bigint, 'PinnedPage'),
    (1562448332510::bigint, 'PageTitleHideOption'),
    (1562869971568::bigint, 'ModerationLog'),
    (1563757595828::bigint, 'UsedUsername'),
    (1565634203341::bigint, 'room'),
    (1571220798684::bigint, 'CustomEmojiCategory'),
    (1572760203493::bigint, 'nodeinfo'),
    (1576269851876::bigint, 'TalkFederationId'),
    (1576869585998::bigint, 'ProxyRemoteFiles'),
    (1579267006611::bigint, 'v12'),
    (1579270193251::bigint, 'v12-2'),
    (1579282808087::bigint, 'v12-3'),
    (1579544426412::bigint, 'v12-4'),
    (1579977526288::bigint, 'v12-5'),
    (1579993013959::bigint, 'v12-6'),
    (1580069531114::bigint, 'v12-7'),
    (1580148575182::bigint, 'v12-8'),
    (1580154400017::bigint, 'v12-9'),
    (1580276619901::bigint, 'v12-10'),
    (1580331224276::bigint, 'v12-11'),
    (1580508795118::bigint, 'v12-12'),
    (1580543501339::bigint, 'v12-13'),
    (1580864313253::bigint, 'v12-14'),
    (1581526429287::bigint, 'user-group-invitation'),
    (1581695816408::bigint, 'user-group-antenna'),
    (1581708415836::bigint, 'drive-user-folder-id-index'),
    (1581979837262::bigint, 'promo'),
    (1582019042083::bigint, 'featured-injecttion'),
    (1582210532752::bigint, 'antenna-exclude'),
    (1582875306439::bigint, 'note-reaction-length'),
    (1585361548360::bigint, 'miauth'),
    (1585385921215::bigint, 'custom-notification'),
    (1585772678853::bigint, 'ap-url'),
    (1586624197029::bigint, 'AddObjectStorageUseProxy'),
    (1586641139527::bigint, 'remote-reaction'),
    (1586708940386::bigint, 'pageAiScript'),
    (1588044505511::bigint, 'hCaptcha'),
    (1589023282116::bigint, 'pubRelay'),
    (1595075960584::bigint, 'blurhash'),
    (1595077605646::bigint, 'blurhash-for-avatar-banner'),
    (1595676934834::bigint, 'instance-icon-url'),
    (1595771249699::bigint, 'word-mute'),
    (1595782306083::bigint, 'word-mute2'),
    (1596548170836::bigint, 'channel'),
    (1596786425167::bigint, 'channel2'),
    (1597230137744::bigint, 'objectStorageSetPublicRead'),
    (1597236229720::bigint, 'IncludingNotificationTypes'),
    (1597385880794::bigint, 'add-sensitive-index'),
    (1597459042300::bigint, 'channel-unread'),
    (1597893996136::bigint, 'ChannelNoteIdDescIndex'),
    (1600353287890::bigint, 'mutingNotificationTypes'),
    (1603094348345::bigint, 'refine-abuse-user-report'),
    (1603095701770::bigint, 'refine-abuse-user-report2'),
    (1603776877564::bigint, 'instance-theme-color'),
    (1603781553011::bigint, 'instance-favicon'),
    (1604821689616::bigint, 'delete-auto-watch'),
    (1605408848373::bigint, 'clip-description'),
    (1605408971051::bigint, 'comments'),
    (1605585339718::bigint, 'instance-pinned-pages'),
    (1605965516823::bigint, 'instance-images'),
    (1606191203881::bigint, 'no-crawle'),
    (1607151207216::bigint, 'instance-pinned-clip'),
    (1607353487793::bigint, 'isExplorable'),
    (1610277136869::bigint, 'registry'),
    (1610277585759::bigint, 'registry2'),
    (1610283021566::bigint, 'registry3'),
    (1611354329133::bigint, 'followersUri'),
    (1611397665007::bigint, 'gallery'),
    (1611547387175::bigint, 'objectStorageS3ForcePathStyle'),
    (1612619156584::bigint, 'announcement-email'),
    (1613155914446::bigint, 'emailNotificationTypes'),
    (1613181457597::bigint, 'user-lang'),
    (1613503367223::bigint, 'use-bigint-for-driveUsage'),
    (1615965918224::bigint, 'chart-v2'),
    (1615966519402::bigint, 'chart-v2-2'),
    (1618637372000::bigint, 'user-last-active-date'),
    (1618639857000::bigint, 'user-hide-online-status'),
    (1619942102890::bigint, 'password-reset'),
    (1620019354680::bigint, 'ad'),
    (1620364649428::bigint, 'ad2'),
    (1621479946000::bigint, 'add-note-indexes'),
    (1622679304522::bigint, 'user-profile-description-length'),
    (1622681548499::bigint, 'log-message-length'),
    (1626509500668::bigint, 'fix-remote-file-proxy'),
    (1629004542760::bigint, 'chart-reindex'),
    (1629024377804::bigint, 'deepl-integration'),
    (1629288472000::bigint, 'fix-channel-userId'),
    (1629512953000::bigint, 'user-is-deleted'),
    (1629778475000::bigint, 'deepl-integration2'),
    (1629833361000::bigint, 'AddShowTLReplies'),
    (1629968054000::bigint, 'userInstanceBlocks'),
    (1633068642000::bigint, 'email-required-for-signup'),
    (1633071909016::bigint, 'user-pending'),
    (1634486652000::bigint, 'user-public-reactions'),
    (1634902659689::bigint, 'delete-log'),
    (1635500777168::bigint, 'note-thread-mute'),
    (1636197624383::bigint, 'ff-visibility'),
    (1636697408073::bigint, 'remove-via-mobile'),
    (1637320813000::bigint, 'forwarded-report'),
    (1639325650583::bigint, 'chart-v3'),
    (1642611822809::bigint, 'emoji-url'),
    (1642613870898::bigint, 'drive-file-webpublic-type'),
    (1643963705770::bigint, 'chart-v4'),
    (1643966656277::bigint, 'chart-v5'),
    (1643967331284::bigint, 'chart-v6'),
    (1644010796173::bigint, 'convert-hard-mutes'),
    (1644058404077::bigint, 'chart-v7'),
    (1644059847460::bigint, 'chart-v8'),
    (1644060125705::bigint, 'chart-v9'),
    (1644073149413::bigint, 'chart-v10'),
    (1644095659741::bigint, 'chart-v11'),
    (1644328606241::bigint, 'chart-v12'),
    (1644331238153::bigint, 'chart-v13'),
    (1644344266289::bigint, 'chart-v14'),
    (1644395759931::bigint, 'instance-theme-color'),
    (1644481657998::bigint, 'chart-v15'),
    (1644551208096::bigint, 'following-indexes'),
    (1645340161439::bigint, 'remove-max-note-text-length'),
    (1645599900873::bigint, 'federation-chart-pubsub'),
    (1646143552768::bigint, 'instance-default-theme'),
    (1646387162108::bigint, 'mute-expires-at'),
    (1646549089451::bigint, 'poll-ended-notification'),
    (1646633030285::bigint, 'chart-federation-active'),
    (1646655454495::bigint, 'remove-instance-drive-columns'),
    (1646732390560::bigint, 'chart-federation-active-sub-pub'),
    (1648548247382::bigint, 'webhook'),
    (1648816172177::bigint, 'webhook-2'),
    (1651224615271::bigint, 'foreign-key'),
    (1652859567549::bigint, 'uniform-themecolor'),
    (1655368940105::bigint, 'nsfw-detection'),
    (1655371960534::bigint, 'nsfw-detection-2'),
    (1655388169582::bigint, 'nsfw-detection-3'),
    (1655393015659::bigint, 'nsfw-detection-4'),
    (1655813815729::bigint, 'driveCapacityOverrideMb'),
    (1655918165614::bigint, 'user-ip'),
    (1656122560740::bigint, 'file-ip'),
    (1656251734807::bigint, 'nsfw-detection-5'),
    (1656328812281::bigint, 'ip-2'),
    (1656408772602::bigint, 'nsfw-detection-6'),
    (1656772790599::bigint, 'user-moderation-note'),
    (1657346559800::bigint, 'active-email-validation'),
    (1664694635394::bigint, 'turnstile'),
    (1665091090561::bigint, 'add-renote-muting'),
    (1669138716634::bigint, 'whetherPushNotifyToSendReadMessage'),
    (1671924750884::bigint, 'RetentionAggregation'),
    (1671926422832::bigint, 'RetentionAggregation2'),
    (1672562400597::bigint, 'PerUserPvChart'),
    (1672703171386::bigint, 'remove-latestRequestSentAt'),
    (1672704017999::bigint, 'remove-lastCommunicatedAt'),
    (1672704136584::bigint, 'remove-latestStatus'),
    (1672822262496::bigint, 'Flash'),
    (1673336077243::bigint, 'PollChoiceLength'),
    (1673500412259::bigint, 'Role'),
    (1673515526953::bigint, 'RoleColor'),
    (1673522856499::bigint, 'RoleIroiro'),
    (1673524604156::bigint, 'RoleLastUsedAt'),
    (1673570377815::bigint, 'RoleConditional'),
    (1673575973645::bigint, 'MetaClean'),
    (1673783015567::bigint, 'Policies'),
    (1673812883772::bigint, 'firstRetrievedAt'),
    (1674086433654::bigint, 'flashScriptLength'),
    (1674118260469::bigint, 'achievement'),
    (1674255666603::bigint, 'loggedInDates'),
    (1675053125067::bigint, 'fixforeignkeyreports'),
    (1675404035646::bigint, 'cleanup'),
    (1675557528704::bigint, 'role-icon-badge'),
    (1676434944993::bigint, 'drop-group'),
    (1676438468213::bigint, 'ad3'),
    (1677054292210::bigint, 'ad4'),
    (1677570181236::bigint, 'role-assignment-expires-at'),
    (1678164627293::bigint, 'per-note-reaction-acceptance'),
    (1678426061773::bigint, 'tweak-varchar-length'),
    (1678427401214::bigint, 'remove-unused'),
    (1678602320354::bigint, 'role-display-order'),
    (1678694614599::bigint, 'sensitive-words'),
    (1678869617549::bigint, 'retention-date-key'),
    (1678945242650::bigint, 'add-props-for-custom-emoji'),
    (1678953978856::bigint, 'clip-favorite'),
    (1679309757174::bigint, 'antenna-active'),
    (1679639483253::bigint, 'enableChartsForRemoteUser'),
    (1679651580149::bigint, 'cleanup'),
    (1679652081809::bigint, 'enableChartsForFederatedInstances'),
    (1680228513388::bigint, 'channelFavorite'),
    (1680238118084::bigint, 'channelNotePining'),
    (1680491187535::bigint, 'cleanup'),
    (1680582195041::bigint, 'cleanup'),
    (1680702787050::bigint, 'UserMemo'),
    (1680775031481::bigint, 'avatar-url-and-banner-url'),
    (1680931179228::bigint, 'account-move'),
    (1681400427971::bigint, 'serverRules'),
    (1681870960239::bigint, 'RoleTLSetting'),
    (1682190963894::bigint, 'movedAt'),
    (1682754135458::bigint, 'preservedUsernames'),
    (1682985520254::bigint, 'channelColor'),
    (1683328299359::bigint, 'channelArchive'),
    (1683682889948::bigint, 'prevent-ai-larning'),
    (1683683083083::bigint, 'public-reactions-default-true'),
    (1683789676867::bigint, 'fix-typo'),
    (1683847157541::bigint, 'UserList'),
    (1683869758873::bigint, 'UserListFavorites'),
    (1684206886988::bigint, 'remove-showTimelineReplies'),
    (1684386446061::bigint, 'emoji-improve'),
    (1685973839966::bigint, 'errorImageUrl'),
    (1688280713783::bigint, 'add-meta-options'),
    (1688720440658::bigint, 'refactor-invite-system'),
    (1688880985544::bigint, 'add-index-to-relations'),
    (1689102832143::bigint, 'nsfw-cache'),
    (1689325027964::bigint, 'UserBlacklistAnntena'),
    (1690417561185::bigint, 'fix-renote-muting'),
    (1690417561186::bigint, 'ChangeCacheRemoteFilesDefault'),
    (1690417561187::bigint, 'Fix'),
    (1690569881926::bigint, 'user-2fa-backup-codes'),
    (1690782653311::bigint, 'SensitiveChannel'),
    (1690796169261::bigint, 'play-visibility'),
    (1691649257651::bigint, 'refine-announcement'),
    (1691657412740::bigint, 'refine-announcement-2'),
    (1691959191872::bigint, 'passkey-support'),
    (1694850832075::bigint, 'server-icons-and-manifest'),
    (1694915420864::bigint, 'clipped-count'),
    (1695260774117::bigint, 'verified-links'),
    (1695288787870::bigint, 'following-notify'),
    (1695440131671::bigint, 'short-name'),
    (1695605508898::bigint, 'mutingNotificationTypes'),
    (1695901659683::bigint, 'note-updated-at'),
    (1695944637565::bigint, 'notificationRecieveConfig'),
    (1696003580220::bigint, 'AddSomeUrls'),
    (1696222183852::bigint, 'withReplies'),
    (1696323464251::bigint, 'user-list-membership'),
    (1696331570827::bigint, 'hibernation'),
    (1696332072038::bigint, 'clean'),
    (1696373953614::bigint, 'meta-cache-settings'),
    (1696388600237::bigint, 'revert-note-edit'),
    (1696405744672::bigint, 'clean-up'),
    (1696569742153::bigint, 'clean-up'),
    (1696581429196::bigint, 'clean-up'),
    (1696743032098::bigint, 'AdsOnStream'),
    (1696807733453::bigint, 'userListUserId'),
    (1696808725134::bigint, 'userListUserId-2'),
    (1697247230117::bigint, 'InstanceSilence'),
    (1697420555911::bigint, 'deleteCreatedAt'),
    (1697436246389::bigint, 'antenna-localOnly'),
    (1697441463087::bigint, 'FollowRequestWithReplies'),
    (1697673894459::bigint, 'note-reactionAndUserPairCache'),
    (1697847397844::bigint, 'avatar-decoration'),
    (1697941908548::bigint, 'avatar-decoration2'),
    (1698041201306::bigint, 'enable-ftt'),
    (1698840138000::bigint, 'add-allow-renote-to-external'),
    (1699141698112::bigint, 'announcement-silence'),
    (1700096812223::bigint, 'enableFanoutTimelineDbFallback'),
    (1700303245007::bigint, 'supportVerifyMailApi'),
    (1700383825690::bigint, 'hard-mute'),
    (1700902349231::bigint, 'add-bday-index'),
    (1702718871541::bigint, 'ffVisibility'),
    (1703209889304::bigint, 'bannedEmailDomains'),
    (1703658526000::bigint, 'supportTrueMailApi'),
    (1704373210054::bigint, 'support-mcaptcha'),
    (1704959805077::bigint, 'bubble-game-record'),
    (1705222772858::bigint, 'optimize-note-index-for-array-column'),
    (1705475608437::bigint, 'reversi'),
    (1705654039457::bigint, 'reversi-2'),
    (1705793785675::bigint, 'reversi-3'),
    (1705794768153::bigint, 'reversi-4'),
    (1705798904141::bigint, 'reversi-5'),
    (1706081514499::bigint, 'reversi-6'),
    (1706791962000::bigint, 'fix-meta-disableRegistration'),
    (1707429690000::bigint, 'prohibited-words'),
    (1707808106310::bigint, 'MakeRepositoryUrlNullable'),
    (1708266695091::bigint, 'repositoryUrl-from-syuilo-to-misskey-dev'),
    (1708399372194::bigint, 'per-instance-mod-note'),
    (1709126576000::bigint, 'optimize-emoji-index'),
    (1710512074000::bigint, 'url-preview-meta'),
    (1710919614510::bigint, 'antenna-exclude-bots'),
    (1713656541000::bigint, 'abuse-report-notification'),
    (1716129964060::bigint, 'ChannelIdDenormalizedForMiPoll'),
    (1716197366117::bigint, 'MediaSilenceForHosts'),
    (1716345015347::bigint, 'NotRespondingSince'),
    (1716447138870::bigint, 'SuspensionStateInsteadOfIsSspended'),
    (1716450883149::bigint, 'RemoveAntennaNotify'),
    (1717117195275::bigint, 'inquiryUrl'),
    (1721666053703::bigint, 'fixDriveUrl'),
    (1723944246767::bigint, 'followedMessage'),
    (1726804538569::bigint, 'reactions-buffering'),
    (1727318020265::bigint, 'enableStatsForFederatedInstances'),
    (1727491883993::bigint, 'user-score'),
    (1727512908322::bigint, 'meta-federation'),
    (1728085812127::bigint, 'refine-abuse-user-report'),
    (1728550878802::bigint, 'testcaptcha'),
    (1728634286056::bigint, 'prohibitedWordsForNameOfUser'),
    (1729333924409::bigint, 'signinRequiredForShowContents'),
    (1729486255072::bigint, 'makeNotesHiddenBefore'),
    (1736230492103::bigint, 'addAntennaHideNotesInSensitiveChannel'),
    (1736686850345::bigint, 'createNoteDraft'),
    (1739006797620::bigint, 'GoogleAnalytics'),
    (1740121393164::bigint, 'system-accounts'),
    (1740129169650::bigint, 'system-accounts-2'),
    (1740133121105::bigint, 'system-accounts-3'),
    (1740993126937::bigint, 'system-accounts-4'),
    (1741279404074::bigint, 'system-accounts-fixup'),
    (1741424411879::bigint, 'user-featured-fixup'),
    (1742203321812::bigint, 'chat'),
    (1742608337548::bigint, 'chat-2'),
    (1742617546147::bigint, 'chat-3'),
    (1742707840715::bigint, 'chat-4'),
    (1742721896936::bigint, 'chat-5'),
    (1742795111958::bigint, 'chat-6'),
    (1743403874305::bigint, 'DeliverSuspendedSoftware'),
    (1743558299182::bigint, 'RoleCopyOnMoveAccount'),
    (1744075766000::bigint, 'excludeNotesInSensitiveChannel'),
    (1745378064470::bigint, 'composite-note-index'),
    (1746330901644::bigint, 'visibleUserGeneratedContentsForNonLoggedInVisitors'),
    (1746422049376::bigint, 'singleUserMode'),
    (1746949539915::bigint, 'migrateSomeConfigFileSettingsToMeta'),
    (1748310233000::bigint, 'addUrlPreviewAllowRedirect'),
    (1750729939704::bigint, 'FixAvatarUrl'),
    (1752502434151::bigint, 'no-action-on-draft-relation'),
    (1752509043847::bigint, 'migration-cleanup'),
    (1753863104203::bigint, 'remoteNotesCleaning'),
    (1753868431598::bigint, 'remove_note_constraints'),
    (1754019326356::bigint, 'tweakDefaultFederationSettings'),
    (1755168347001::bigint, 'PageCountInNote'),
    (1755574887486::bigint, 'entrancePageStyle'),
    (1756062689648::bigint, 'NonCascadingPageEyeCatching'),
    (1757823175259::bigint, 'sensitive-ad'),
    (1758677617888::bigint, 'scheduled-post'),
    (1760607435831::bigint, 'RoleBadgesRemoteUsers'),
    (1760790899857::bigint, 'unnecessary-null-default'),
    (1761569941833::bigint, 'add-channel-muting'),
    (1767169026317::bigint, 'birthday-index')
) AS v(ts, n)
WHERE NOT EXISTS (SELECT 1 FROM "migrations" m WHERE m.name = v.n);
