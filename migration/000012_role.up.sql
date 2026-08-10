-- role: ロールベースの権限管理 (Misskey 互換)
-- 重複判定は他の enum (000001 / 000007 / 000009) と同じく duplicate_object 例外で
-- 行う。`pg_type WHERE typname = ...` は **schema を見ない** ので、別 schema に
-- 同名の型があるだけで作成を飛ばし、直後の CREATE TABLE が
-- 「type does not exist」で落ちる。public しか使わない本番では差が出ないが、
-- schema を分ける test 環境で顕在化した (#2450)。
DO $$ BEGIN
    CREATE TYPE role_target_enum AS ENUM ('manual', 'conditional');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS "role" (
    "id" varchar(32) PRIMARY KEY,
    "updatedAt" timestamp with time zone NOT NULL DEFAULT now(),
    "lastUsedAt" timestamp with time zone NOT NULL DEFAULT now(),
    "name" varchar(256) NOT NULL,
    "description" varchar(1024) NOT NULL DEFAULT '',
    "color" varchar(256),
    "iconUrl" varchar(512),
    "target" role_target_enum NOT NULL DEFAULT 'manual',
    "condFormula" jsonb NOT NULL DEFAULT '{}',
    "isPublic" boolean NOT NULL DEFAULT false,
    "asBadge" boolean NOT NULL DEFAULT false,
    "isModerator" boolean NOT NULL DEFAULT false,
    "isAdministrator" boolean NOT NULL DEFAULT false,
    "isExplorable" boolean NOT NULL DEFAULT false,
    "preserveAssignmentOnMoveAccount" boolean NOT NULL DEFAULT false,
    "canEditMembersByModerator" boolean NOT NULL DEFAULT false,
    "displayOrder" integer NOT NULL DEFAULT 0,
    "policies" jsonb NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS "IDX_role_isPublic" ON "role" ("isPublic");
CREATE INDEX IF NOT EXISTS "IDX_role_target" ON "role" ("target");

-- role_assignment: ユーザーへのロール割り当て
CREATE TABLE IF NOT EXISTS "role_assignment" (
    "id" varchar(32) PRIMARY KEY,
    "userId" varchar(32) NOT NULL REFERENCES "user"("id") ON DELETE CASCADE,
    "roleId" varchar(32) NOT NULL REFERENCES "role"("id") ON DELETE CASCADE,
    "expiresAt" timestamp with time zone
);

CREATE INDEX IF NOT EXISTS "IDX_role_assignment_userId" ON "role_assignment" ("userId");
CREATE INDEX IF NOT EXISTS "IDX_role_assignment_roleId" ON "role_assignment" ("roleId");
CREATE INDEX IF NOT EXISTS "IDX_role_assignment_expiresAt" ON "role_assignment" ("expiresAt");
CREATE UNIQUE INDEX IF NOT EXISTS "IDX_role_assignment_userId_roleId" ON "role_assignment" ("userId", "roleId");
