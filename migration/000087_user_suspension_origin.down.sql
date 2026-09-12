-- 由来の記録を落とすだけ。`user.isSuspended` そのものには影響しないので、
-- 凍結状態は保たれる。
--
-- data loss: 「どちらの判断で凍結されたか」は復元できない。down 後に
-- 再び up すると、その時点で凍結されている行はすべて local 由来として
-- 記録し直される (リモート由来のものも local になる = 安全側)。
DROP TABLE IF EXISTS "user_suspension_origin";
