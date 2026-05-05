-- Initial schema for mk-go
-- Compatible with existing Misskey TypeScript database schema

-- Enum types
DO $$ BEGIN
    CREATE TYPE note_visibility_enum AS ENUM ('public', 'home', 'followers', 'specified');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    CREATE TYPE following_visibility_enum AS ENUM ('public', 'followers', 'private');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    CREATE TYPE followers_visibility_enum AS ENUM ('public', 'followers', 'private');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- user
CREATE TABLE IF NOT EXISTS "user" (
    "id" varchar(32) PRIMARY KEY,
    "updatedAt" timestamp with time zone,
    "lastFetchedAt" timestamp with time zone,
    "lastActiveDate" timestamp with time zone,
    "hideOnlineStatus" boolean NOT NULL DEFAULT false,
    "username" varchar(128) NOT NULL,
    "usernameLower" varchar(128) NOT NULL,
    "name" varchar(128),
    "followersCount" integer NOT NULL DEFAULT 0,
    "followingCount" integer NOT NULL DEFAULT 0,
    "movedToUri" varchar(512),
    "movedAt" timestamp with time zone,
    "alsoKnownAs" text,
    "notesCount" integer NOT NULL DEFAULT 0,
    "avatarId" varchar(32),
    "bannerId" varchar(32),
    "avatarUrl" varchar(1024),
    "bannerUrl" varchar(512),
    "avatarBlurhash" varchar(128),
    "bannerBlurhash" varchar(128),
    "avatarDecorations" jsonb NOT NULL DEFAULT '[]',
    "tags" varchar(128)[] NOT NULL DEFAULT '{}',
    "score" integer NOT NULL DEFAULT 0,
    "isSuspended" boolean NOT NULL DEFAULT false,
    "isLocked" boolean NOT NULL DEFAULT false,
    "isBot" boolean NOT NULL DEFAULT false,
    "isCat" boolean NOT NULL DEFAULT false,
    "isExplorable" boolean NOT NULL DEFAULT true,
    "isHibernated" boolean NOT NULL DEFAULT false,
    "requireSigninToViewContents" boolean NOT NULL DEFAULT false,
    "makeNotesFollowersOnlyBefore" integer,
    "makeNotesHiddenBefore" integer,
    "isDeleted" boolean NOT NULL DEFAULT false,
    "emojis" varchar(128)[] NOT NULL DEFAULT '{}',
    "chatScope" varchar(128) NOT NULL DEFAULT 'mutual',
    "host" varchar(128),
    "inbox" varchar(512),
    "sharedInbox" varchar(512),
    "featured" varchar(512),
    "uri" varchar(512),
    "followersUri" varchar(512),
    "token" char(16)
);

CREATE INDEX IF NOT EXISTS "IDX_user_usernameLower" ON "user" ("usernameLower");
CREATE INDEX IF NOT EXISTS "IDX_user_host" ON "user" ("host");
CREATE UNIQUE INDEX IF NOT EXISTS "IDX_user_usernameLower_host_unique" ON "user" ("usernameLower", "host") WHERE "host" IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS "IDX_user_usernameLower_local_unique" ON "user" ("usernameLower") WHERE "host" IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS "IDX_user_token" ON "user" ("token");

-- drive_folder
CREATE TABLE IF NOT EXISTS "drive_folder" (
    "id" varchar(32) PRIMARY KEY,
    "name" varchar(128) NOT NULL,
    "userId" varchar(32) REFERENCES "user"("id") ON DELETE CASCADE,
    "parentId" varchar(32) REFERENCES "drive_folder"("id") ON DELETE SET NULL
);

-- drive_file
CREATE TABLE IF NOT EXISTS "drive_file" (
    "id" varchar(32) PRIMARY KEY,
    "userId" varchar(32) REFERENCES "user"("id") ON DELETE SET NULL,
    "userHost" varchar(128),
    "md5" varchar(32) NOT NULL,
    "name" varchar(256) NOT NULL,
    "type" varchar(128) NOT NULL,
    "size" integer NOT NULL,
    "comment" varchar(512),
    "blurhash" varchar(128),
    "properties" jsonb NOT NULL DEFAULT '{}',
    "storedInternal" boolean NOT NULL,
    "url" varchar(1024) NOT NULL,
    "thumbnailUrl" varchar(512),
    "webpublicUrl" varchar(512),
    "webpublicType" varchar(128),
    "accessKey" varchar(256),
    "thumbnailAccessKey" varchar(256),
    "webpublicAccessKey" varchar(256),
    "uri" varchar(1024),
    "src" varchar(1024),
    "folderId" varchar(32) REFERENCES "drive_folder"("id") ON DELETE SET NULL,
    "isSensitive" boolean NOT NULL DEFAULT false,
    "maybeSensitive" boolean NOT NULL DEFAULT false,
    "maybePorn" boolean NOT NULL DEFAULT false,
    "isLink" boolean NOT NULL DEFAULT false,
    "requestHeaders" jsonb NOT NULL DEFAULT '{}',
    "requestIp" varchar(128)
);

CREATE UNIQUE INDEX IF NOT EXISTS "IDX_drive_file_accessKey" ON "drive_file" ("accessKey");
CREATE UNIQUE INDEX IF NOT EXISTS "IDX_drive_file_thumbnailAccessKey" ON "drive_file" ("thumbnailAccessKey");
CREATE UNIQUE INDEX IF NOT EXISTS "IDX_drive_file_webpublicAccessKey" ON "drive_file" ("webpublicAccessKey");
CREATE INDEX IF NOT EXISTS "IDX_drive_file_userId" ON "drive_file" ("userId");

-- user avatar/banner FK (drive_file must exist first)
ALTER TABLE "user" ADD CONSTRAINT "FK_user_avatarId" FOREIGN KEY ("avatarId") REFERENCES "drive_file"("id") ON DELETE SET NULL;
ALTER TABLE "user" ADD CONSTRAINT "FK_user_bannerId" FOREIGN KEY ("bannerId") REFERENCES "drive_file"("id") ON DELETE SET NULL;

-- user_profile
CREATE TABLE IF NOT EXISTS "user_profile" (
    "userId" varchar(32) PRIMARY KEY REFERENCES "user"("id") ON DELETE CASCADE,
    "location" varchar(128),
    "birthday" char(10),
    "description" varchar(2048),
    "followedMessage" varchar(256),
    "fields" jsonb NOT NULL DEFAULT '[]',
    "verifiedLinks" varchar[] NOT NULL DEFAULT '{}',
    "lang" varchar(32),
    "url" varchar(512),
    "email" varchar(128),
    "emailVerifyCode" varchar(128),
    "emailVerified" boolean NOT NULL DEFAULT false,
    "emailNotificationTypes" jsonb NOT NULL DEFAULT '["follow","receiveFollowRequest"]',
    "publicReactions" boolean NOT NULL DEFAULT true,
    "followingVisibility" following_visibility_enum NOT NULL DEFAULT 'public',
    "followersVisibility" followers_visibility_enum NOT NULL DEFAULT 'public',
    "twoFactorTempSecret" varchar(128),
    "twoFactorSecret" varchar(128),
    "twoFactorEnabled" boolean NOT NULL DEFAULT false,
    "securityKeysAvailable" boolean NOT NULL DEFAULT false,
    "usePasswordLessLogin" boolean NOT NULL DEFAULT false,
    "password" varchar(128),
    "moderationNote" varchar(8192) NOT NULL DEFAULT '',
    "autoAcceptFollowed" boolean NOT NULL DEFAULT false,
    "noCrawle" boolean NOT NULL DEFAULT false,
    "preventAiLearning" boolean NOT NULL DEFAULT true,
    "alwaysMarkNsfw" boolean NOT NULL DEFAULT false,
    "autoSensitive" boolean NOT NULL DEFAULT false,
    "carefulBot" boolean NOT NULL DEFAULT false,
    "injectFeaturedNote" boolean NOT NULL DEFAULT true,
    "receiveAnnouncementEmail" boolean NOT NULL DEFAULT true,
    "pinnedPageId" varchar(32),
    "enableWordMute" boolean NOT NULL DEFAULT false,
    "mutedWords" jsonb NOT NULL DEFAULT '[]',
    "hardMutedWords" jsonb NOT NULL DEFAULT '[]',
    "mutedInstances" jsonb NOT NULL DEFAULT '[]',
    "notificationRecieveConfig" jsonb NOT NULL DEFAULT '{}',
    "loggedInDates" varchar(32)[] NOT NULL DEFAULT '{}',
    "achievements" jsonb NOT NULL DEFAULT '[]',
    "userHost" varchar(128)
);

-- user_keypair
CREATE TABLE IF NOT EXISTS "user_keypair" (
    "userId" varchar(32) PRIMARY KEY REFERENCES "user"("id") ON DELETE CASCADE,
    "publicKey" varchar(4096) NOT NULL,
    "privateKey" varchar(4096) NOT NULL
);

-- note
CREATE TABLE IF NOT EXISTS "note" (
    "id" varchar(32) PRIMARY KEY,
    "replyId" varchar(32),
    "renoteId" varchar(32),
    "threadId" varchar(256),
    "text" text,
    "name" varchar(256),
    "cw" varchar(512),
    "userId" varchar(32) NOT NULL REFERENCES "user"("id") ON DELETE CASCADE,
    "localOnly" boolean NOT NULL DEFAULT false,
    "reactionAcceptance" varchar(64),
    "renoteCount" smallint NOT NULL DEFAULT 0,
    "repliesCount" smallint NOT NULL DEFAULT 0,
    "clippedCount" smallint NOT NULL DEFAULT 0,
    "pageCount" smallint NOT NULL DEFAULT 0,
    "reactions" jsonb NOT NULL DEFAULT '{}',
    "visibility" note_visibility_enum NOT NULL,
    "uri" varchar(512),
    "url" varchar(512),
    "fileIds" varchar(32)[] NOT NULL DEFAULT '{}',
    "attachedFileTypes" varchar(256)[] NOT NULL DEFAULT '{}',
    "visibleUserIds" varchar(32)[] NOT NULL DEFAULT '{}',
    "mentions" varchar(32)[] NOT NULL DEFAULT '{}',
    "mentionedRemoteUsers" text NOT NULL DEFAULT '[]',
    "reactionAndUserPairCache" varchar(1024)[] NOT NULL DEFAULT '{}',
    "emojis" varchar(128)[] NOT NULL DEFAULT '{}',
    "tags" varchar(128)[] NOT NULL DEFAULT '{}',
    "hasPoll" boolean NOT NULL DEFAULT false,
    "channelId" varchar(32),
    "userHost" varchar(128),
    "replyUserId" varchar(32),
    "replyUserHost" varchar(128),
    "renoteUserId" varchar(32),
    "renoteUserHost" varchar(128),
    "renoteChannelId" varchar(32)
);

ALTER TABLE "note" ADD CONSTRAINT "FK_note_replyId" FOREIGN KEY ("replyId") REFERENCES "note"("id") ON DELETE SET NULL;
ALTER TABLE "note" ADD CONSTRAINT "FK_note_renoteId" FOREIGN KEY ("renoteId") REFERENCES "note"("id") ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS "IDX_note_userId" ON "note" ("userId");
CREATE INDEX IF NOT EXISTS "IDX_note_visibility" ON "note" ("visibility");
CREATE INDEX IF NOT EXISTS "IDX_note_userHost" ON "note" ("userHost");

-- poll
CREATE TABLE IF NOT EXISTS "poll" (
    "noteId" varchar(32) PRIMARY KEY REFERENCES "note"("id") ON DELETE CASCADE,
    "expiresAt" timestamp with time zone,
    "multiple" boolean NOT NULL,
    "choices" varchar(256)[] NOT NULL DEFAULT '{}',
    "votes" integer[] NOT NULL,
    "noteVisibility" note_visibility_enum,
    "userId" varchar(32),
    "userHost" varchar(128),
    "channelId" varchar(32)
);

-- note_reaction
CREATE TABLE IF NOT EXISTS "note_reaction" (
    "id" varchar(32) PRIMARY KEY,
    "userId" varchar(32) NOT NULL REFERENCES "user"("id") ON DELETE CASCADE,
    "noteId" varchar(32) NOT NULL REFERENCES "note"("id") ON DELETE CASCADE,
    "reaction" varchar(260) NOT NULL
);

CREATE INDEX IF NOT EXISTS "IDX_note_reaction_userId" ON "note_reaction" ("userId");
CREATE INDEX IF NOT EXISTS "IDX_note_reaction_noteId" ON "note_reaction" ("noteId");
CREATE UNIQUE INDEX IF NOT EXISTS "IDX_note_reaction_userId_noteId" ON "note_reaction" ("userId", "noteId");

-- following
CREATE TABLE IF NOT EXISTS "following" (
    "id" varchar(32) PRIMARY KEY,
    "followeeId" varchar(32) NOT NULL REFERENCES "user"("id") ON DELETE CASCADE,
    "followerId" varchar(32) NOT NULL REFERENCES "user"("id") ON DELETE CASCADE,
    "isFollowerHibernated" boolean NOT NULL DEFAULT false,
    "withReplies" boolean NOT NULL DEFAULT false,
    "notify" varchar(32),
    "followerHost" varchar(128),
    "followerInbox" varchar(512),
    "followerSharedInbox" varchar(512),
    "followeeHost" varchar(128),
    "followeeInbox" varchar(512),
    "followeeSharedInbox" varchar(512)
);

CREATE INDEX IF NOT EXISTS "IDX_following_followeeId" ON "following" ("followeeId");
CREATE INDEX IF NOT EXISTS "IDX_following_followerId" ON "following" ("followerId");
CREATE UNIQUE INDEX IF NOT EXISTS "IDX_following_followerId_followeeId" ON "following" ("followerId", "followeeId");

-- access_token
CREATE TABLE IF NOT EXISTS "access_token" (
    "id" varchar(32) PRIMARY KEY,
    "lastUsedAt" timestamp with time zone,
    "token" varchar(128) NOT NULL,
    "session" varchar(128),
    "hash" varchar(128) NOT NULL,
    "userId" varchar(32) NOT NULL REFERENCES "user"("id") ON DELETE CASCADE,
    "appId" varchar(32),
    "name" varchar(128),
    "description" varchar(512),
    "iconUrl" varchar(512),
    "permission" varchar(64)[] NOT NULL DEFAULT '{}',
    "fetched" boolean NOT NULL DEFAULT false
);

CREATE INDEX IF NOT EXISTS "IDX_access_token_hash" ON "access_token" ("hash");
CREATE INDEX IF NOT EXISTS "IDX_access_token_userId" ON "access_token" ("userId");

-- emoji
CREATE TABLE IF NOT EXISTS "emoji" (
    "id" varchar(32) PRIMARY KEY,
    "updatedAt" timestamp with time zone,
    "name" varchar(128) NOT NULL,
    "host" varchar(128),
    "category" varchar(128),
    "originalUrl" varchar(512) NOT NULL,
    "publicUrl" varchar(512) NOT NULL DEFAULT '',
    "uri" varchar(512),
    "type" varchar(64),
    "aliases" varchar(128)[] NOT NULL DEFAULT '{}',
    "license" varchar(1024),
    "localOnly" boolean NOT NULL DEFAULT false,
    "isSensitive" boolean NOT NULL DEFAULT false,
    "roleIdsThatCanBeUsedThisEmojiAsReaction" varchar(128)[] NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS "IDX_emoji_name" ON "emoji" ("name");
CREATE INDEX IF NOT EXISTS "IDX_emoji_host" ON "emoji" ("host");
CREATE UNIQUE INDEX IF NOT EXISTS "IDX_emoji_name_host" ON "emoji" ("name", "host");

-- instance
CREATE TABLE IF NOT EXISTS "instance" (
    "id" varchar(32) PRIMARY KEY,
    "firstRetrievedAt" timestamp with time zone NOT NULL,
    "host" varchar(128) NOT NULL,
    "usersCount" integer NOT NULL DEFAULT 0,
    "notesCount" integer NOT NULL DEFAULT 0,
    "followingCount" integer NOT NULL DEFAULT 0,
    "followersCount" integer NOT NULL DEFAULT 0,
    "latestRequestReceivedAt" timestamp with time zone,
    "isNotResponding" boolean NOT NULL DEFAULT false,
    "notRespondingSince" timestamp with time zone,
    "suspensionState" varchar(64) NOT NULL DEFAULT 'none',
    "softwareName" varchar(64),
    "softwareVersion" varchar(64),
    "openRegistrations" boolean,
    "name" varchar(256),
    "description" varchar(4096),
    "maintainerName" varchar(128),
    "maintainerEmail" varchar(256),
    "iconUrl" varchar(256),
    "faviconUrl" varchar(256),
    "themeColor" varchar(64),
    "infoUpdatedAt" timestamp with time zone,
    "moderationNote" varchar(16384) NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX IF NOT EXISTS "IDX_instance_host" ON "instance" ("host");

-- meta (singleton)
CREATE TABLE IF NOT EXISTS "meta" (
    "id" varchar(32) PRIMARY KEY,
    "rootUserId" varchar(32),
    "name" varchar(1024),
    "shortName" varchar(64),
    "description" varchar(1024),
    "maintainerName" varchar(1024),
    "maintainerEmail" varchar(1024),
    "disableRegistration" boolean NOT NULL DEFAULT true,
    "langs" varchar(1024)[] NOT NULL DEFAULT '{}',
    "pinnedUsers" varchar(1024)[] NOT NULL DEFAULT '{}',
    "hiddenTags" varchar(1024)[] NOT NULL DEFAULT '{}',
    "blockedHosts" varchar(1024)[] NOT NULL DEFAULT '{}',
    "sensitiveWords" varchar(1024)[] NOT NULL DEFAULT '{}',
    "prohibitedWords" varchar(1024)[] NOT NULL DEFAULT '{}',
    "silencedHosts" varchar(1024)[] NOT NULL DEFAULT '{}',
    "themeColor" varchar(1024),
    "bannerUrl" varchar(1024),
    "backgroundImageUrl" varchar(1024),
    "logoImageUrl" varchar(1024),
    "iconUrl" varchar(1024),
    "cacheRemoteFiles" boolean NOT NULL DEFAULT false,
    "cacheRemoteSensitiveFiles" boolean NOT NULL DEFAULT true,
    "emailRequiredForSignup" boolean NOT NULL DEFAULT false,
    "enableHcaptcha" boolean NOT NULL DEFAULT false,
    "hcaptchaSiteKey" varchar(1024),
    "hcaptchaSecretKey" varchar(1024),
    "enableRecaptcha" boolean NOT NULL DEFAULT false,
    "recaptchaSiteKey" varchar(1024),
    "recaptchaSecretKey" varchar(1024),
    "enableTurnstile" boolean NOT NULL DEFAULT false,
    "turnstileSiteKey" varchar(1024),
    "turnstileSecretKey" varchar(1024),
    "enableEmail" boolean NOT NULL DEFAULT false,
    "email" varchar(1024),
    "smtpSecure" boolean NOT NULL DEFAULT false,
    "smtpHost" varchar(1024),
    "smtpPort" integer,
    "smtpUser" varchar(1024),
    "smtpPass" varchar(1024),
    "enableServiceWorker" boolean NOT NULL DEFAULT false,
    "swPublicKey" varchar(1024),
    "swPrivateKey" varchar(1024),
    "useObjectStorage" boolean NOT NULL DEFAULT false,
    "objectStorageBucket" varchar(1024),
    "objectStoragePrefix" varchar(1024),
    "objectStorageBaseUrl" varchar(1024),
    "objectStorageEndpoint" varchar(1024),
    "objectStorageRegion" varchar(1024),
    "objectStorageAccessKey" varchar(1024),
    "objectStorageSecretKey" varchar(1024),
    "objectStoragePort" integer,
    "objectStorageUseSSL" boolean NOT NULL DEFAULT true,
    "objectStorageUseProxy" boolean NOT NULL DEFAULT true,
    "objectStorageSetPublicRead" boolean NOT NULL DEFAULT false,
    "objectStorageS3ForcePathStyle" boolean NOT NULL DEFAULT true,
    "policies" jsonb NOT NULL DEFAULT '{}',
    "serverRules" varchar(280)[] NOT NULL DEFAULT '{}',
    "termsOfServiceUrl" varchar(1024),
    "repositoryUrl" varchar(1024),
    "feedbackUrl" varchar(1024),
    "impressumUrl" varchar(1024),
    "privacyPolicyUrl" varchar(1024),
    "federation" varchar(128) NOT NULL DEFAULT 'none',
    "federationHosts" varchar(1024)[] NOT NULL DEFAULT '{}',
    "enableFanoutTimeline" boolean NOT NULL DEFAULT true,
    "enableFanoutTimelineDbFallback" boolean NOT NULL DEFAULT true,
    "proxyRemoteFiles" boolean NOT NULL DEFAULT true,
    "signToActivityPubGet" boolean NOT NULL DEFAULT true
);

-- schema_migrations tracking table for golang-migrate
CREATE TABLE IF NOT EXISTS "schema_migrations" (
    "version" bigint NOT NULL,
    "dirty" boolean NOT NULL,
    PRIMARY KEY ("version")
);
