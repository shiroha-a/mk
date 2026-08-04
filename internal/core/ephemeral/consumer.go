package ephemeral

// この定義は「materializer を受け取る側」が書くべき narrow interface の雛形
// ではなく、複数サービスで同じ形になる EnsureNote / EnsureUser の契約を
// 一箇所で説明するための doc 置き場。各サービスは自前で同名の interface を
// 宣言しており (mk-go の慣習: consumer 側で narrow interface を定義)、この
// ファイルはその形が揃っていることを示す。
//
// materialize を呼ぶ場所の規則 (#2332):
//
//	他の行から **外部キーで参照される必要が生じたとき** に限り DB へ起こす。
//
// note.id を参照する外部キーからトリガーは機械的に導出できる。
//
//	note_reaction.noteId       -> core/reaction
//	note.replyId / renoteId    -> core/note (create)
//	note_favorite.noteId       -> favorite
//	clip_note.noteId           -> core/clip
//	antenna_note_unread.noteId -> core/antenna
//	note_unread.noteId         -> unread
//	poll_vote.noteId           -> core/poll
//
// 著者だけを要求する契機もある。いずれも user への外部キーで、ノート行は要らない。
//
//	muting.muteeId / blocking.blockeeId    -> core/muting, core/blocking
//	abuse_user_report.targetUserId         -> abuse report
//
// **閲覧では呼ばない。** リンクを踏まれるたびに永続化されると、DB を膨らませ
// ない目的が崩れる。読み取り経路は Redis から返し、TTL を打ち直す。
