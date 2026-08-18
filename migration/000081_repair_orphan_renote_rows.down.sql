-- #2623 の後始末は巻き戻せない。
--
-- up 側は (1) 中身の無い孤児行を削除し、(2) 残した行の切れたリンク痕跡
-- (renoteUserId / renoteUserHost / renoteChannelId / replyUserId /
-- replyUserHost) を NULL にする。どちらも**元の値を保存していない**ので、
-- ここで復元する手段は無い。
--
-- 意図的に no-op にしてある。「巻き戻せないので down を用意しない」形にすると
-- golang-migrate が down 方向で止まり、000080 まで戻せなくなるため。
SELECT 1;
