package reversi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/api/apierr"
	corereversi "github.com/shiroha-a/mk/internal/core/reversi"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"gorm.io/datatypes"
)

// FederationDeliverer delivers AP activities to remote users.
// 循環依存を避けるため interface で定義。実装は core/federation.DeliverService。
type FederationDeliverer interface {
	DeliverToUser(signerUserID string, recipient *model.User, body []byte) error
}

// ReversiStreamPublisher publishes `invited` events to a local user's
// reversi stream topic (#417 P2)。実装は internal/stream.ReversiGamePublisher。
type ReversiStreamPublisher interface {
	PublishInvited(targetUserID string, inviter *model.User)
}

// Handler handles reversi/* endpoints.
type Handler struct {
	repo         repository.ReversiRepository
	svc          *corereversi.Service
	idGen        id.Generator
	baseURL      string
	deliverer    FederationDeliverer
	fedCache     *corereversi.FederationIDCache
	userRepo     repository.UserRepository
	streamPub    ReversiStreamPublisher
	fedChecker   FederationAvailabilityChecker
	remoteLookup RemoteUserLookup
}

// FederationAvailabilityChecker determines whether a remote host advertises
// a compatible reversiVersion via its nodeinfo.
// 実装は core/reversi.FederationChecker。
type FederationAvailabilityChecker interface {
	Available(ctx context.Context, host string) bool
}

// RemoteUserLookup resolves an uncached remote user identified by acct
// (`@user@host`) via WebFinger + ActivityPub Person fetch.
// 実装は core/federation.RemoteUserResolver。
type RemoteUserLookup interface {
	ResolveByUsernameHost(username, host string) (*model.User, error)
}

// NewHandler creates a new reversi handler.
func NewHandler(repo repository.ReversiRepository, idGen id.Generator) *Handler {
	return &Handler{repo: repo, idGen: idGen}
}

// SetService attaches the core reversi service. 必須ではないが設定されていれば
// Surrender 等の player action が service 経由で動く (IsStarted バリデーション +
// WebSocket 通知が効くようになる)。nil の場合は従来の repo 直接操作にフォール
// バックするので既存テスト互換。
func (h *Handler) SetService(svc *corereversi.Service) {
	h.svc = svc
}

// SetFederationChecker attaches a FederationAvailabilityChecker used to
// gate outbound Invite delivery when the remote host does not advertise a
// compatible reversiVersion (#417 P3)。未設定なら常に送信を許可する
// (従来互換)。
func (h *Handler) SetFederationChecker(c FederationAvailabilityChecker) {
	h.fedChecker = c
}

// SetRemoteUserLookup attaches a webfinger-capable remote user resolver so
// that /match can accept `@user@host` acct for users not yet cached locally
// (#417 P3 Enhancement)。
func (h *Handler) SetRemoteUserLookup(r RemoteUserLookup) {
	h.remoteLookup = r
}

// SetStreamPublisher attaches a per-user reversi stream publisher used for
// local-side `invited` events (#417 P2)。
//
// NOTE: local invite 経路でこのパブリッシャを使うには userRepo が必要で、
// userRepo は SetFederation で設定される。プロダクションは router.go で
// 両方同時に配線するので問題無いが、片方だけ呼んだ場合 publish は skip
// される (Match 側で h.userRepo != nil guard がある)。
func (h *Handler) SetStreamPublisher(p ReversiStreamPublisher) {
	h.streamPub = p
}

// SetFederation attaches federation support.
func (h *Handler) SetFederation(baseURL string, deliverer FederationDeliverer, fedCache *corereversi.FederationIDCache, userRepo repository.UserRepository) {
	h.baseURL = baseURL
	h.deliverer = deliverer
	h.fedCache = fedCache
	h.userRepo = userRepo
}

func packGame(g *model.ReversiGame, idGen id.Generator) map[string]any {
	result := map[string]any{
		"id":                   g.ID,
		"user1Id":              g.User1ID,
		"user2Id":              g.User2ID,
		"user1Ready":           g.User1Ready,
		"user2Ready":           g.User2Ready,
		"black":                g.Black,
		"isStarted":            g.IsStarted,
		"isEnded":              g.IsEnded,
		"winnerId":             g.WinnerID,
		"surrenderedUserId":    g.SurrenderedUserID,
		"timeoutUserId":        g.TimeoutUserID,
		"timeLimitForEachTurn": g.TimeLimitForEachTurn,
		"noIrregularRules":     g.NoIrregularRules,
		"isLlotheo":            g.IsLlotheo,
		"canPutEverywhere":     g.CanPutEverywhere,
		"loopedBoard":          g.LoopedBoard,
		"map":                  g.Map,
		"bw":                   g.BW,
		"startedAt":            g.StartedAt,
		"endedAt":              g.EndedAt,
		"logs":                 g.Logs,
		"crc32":                g.CRC32,
	}
	if t, err := idGen.ParseTime(g.ID); err == nil {
		result["createdAt"] = t.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	if g.User1 != nil {
		result["user1"] = entity.PackUserLite(g.User1)
	}
	if g.User2 != nil {
		result["user2"] = entity.PackUserLite(g.User2)
	}
	// winner は upstream Misskey TS の ReversiGameEntityService.packDetail と
	// 同じく winnerId から UserLite を派生させる (#649)。frontend は
	// `v-if="game.winner"` で勝敗表示を切り替えるため、winnerId だけでは
	// draw に倒れる。
	if g.WinnerID != nil {
		if g.User1 != nil && g.User1.ID == *g.WinnerID {
			result["winner"] = entity.PackUserLite(g.User1)
		} else if g.User2 != nil && g.User2.ID == *g.WinnerID {
			result["winner"] = entity.PackUserLite(g.User2)
		}
	}
	return result
}

// 標準8x8盤面
var defaultMap = pq.StringArray{
	"--------",
	"--------",
	"--------",
	"---wb---",
	"---bw---",
	"--------",
	"--------",
	"--------",
}

// resolveAcct converts an acct form (`@user` or `@user@host`) into a local
// user id。最初に UserRepository を引き、remote でキャッシュ未取込ならば
// RemoteUserLookup (WebFinger + AP Person fetch) にフォールバックする
// (#417 P3 Enhancement: リモートユーザーを招待できるようにする)。
func (h *Handler) resolveAcct(acct string) (string, error) {
	trimmed := strings.TrimPrefix(acct, "@")
	if trimmed == "" {
		return "", errors.New("empty acct")
	}
	username := trimmed
	var host string
	if at := strings.IndexByte(trimmed, '@'); at >= 0 {
		username = trimmed[:at]
		host = strings.ToLower(trimmed[at+1:])
	}
	// DB 側は username_lower 比較、WebFinger は RFC 7033 case-insensitive、
	// AP resolver も canonical username を再取得するので username を一度だけ
	// 小文字化して両経路で同じ値を使う (#417 P3 Devin review)。
	username = strings.ToLower(username)
	var hostPtr *string
	if host != "" {
		hostPtr = &host
	}
	if u, err := h.userRepo.FindByUsernameLower(username, hostPtr); err == nil {
		return u.ID, nil
	}
	// ローカル DB に無い remote user → WebFinger で取り込む。
	if host != "" && h.remoteLookup != nil {
		if u, err := h.remoteLookup.ResolveByUsernameHost(username, host); err == nil && u != nil {
			return u.ID, nil
		}
	}
	return "", errors.New("user not found")
}

// Games handles POST /api/reversi/games — list games.
// CherryPick 本家互換で sinceId / untilId / my の keyset pagination を受ける。
// my=true (要認証) は自分が User1 or User2 のゲーム、それ以外は isStarted=true
// のゲームを返す。pagination を無視すると frontend の無限スクロールが同じ
// ページを繰り返しロードするので必須 (#417 P1 UDS 検証で発覚)。
func (h *Handler) Games(c echo.Context) error {
	var req struct {
		Limit     int    `json:"limit"`
		SinceID   string `json:"sinceId"`
		UntilID   string `json:"untilId"`
		SinceDate *int64 `json:"sinceDate"`
		UntilDate *int64 `json:"untilDate"`
		My        bool   `json:"my"`
	}
	_ = c.Bind(&req)
	if req.Limit <= 0 {
		req.Limit = 10
	}
	if req.Limit > 100 {
		req.Limit = 100
	}
	// sinceDate / untilDate を aidx prefix に正規化 (#1173)。
	sinceID, untilID := id.NormalizeCursor(req.SinceID, req.UntilID, req.SinceDate, req.UntilDate)
	viewer := middleware.GetUser(c)
	var games []*model.ReversiGame
	if req.My && viewer != nil {
		games, _ = h.repo.ListByUserCursor(viewer.ID, sinceID, untilID, req.Limit)
	} else {
		games, _ = h.repo.ListStartedCursor(sinceID, untilID, req.Limit)
	}
	out := make([]map[string]any, len(games))
	for i, g := range games {
		out[i] = packGame(g, h.idGen)
	}
	return c.JSON(http.StatusOK, out)
}

// Invitations handles POST /api/reversi/invitations — list pending invitations.
// レスポンス shape は CherryPick 本家と同じ UserLite[] (招待者一覧)。Game[] を
// 返すと Misskey フロントエンドの matching UI が期待値と食い違って無限 loading
// になる (#417 P1 deploy で発覚)。viewer が User2 (招待される側) となっている
// 未開始 / 未終了 game の User1 を UserLite として返す。
func (h *Handler) Invitations(c echo.Context) error {
	user := middleware.GetUser(c)
	games, _ := h.repo.ListByUser(user.ID, 20)
	inviters := make([]entity.UserLite, 0)
	seen := make(map[string]struct{})
	for _, g := range games {
		if g.IsStarted || g.IsEnded {
			continue
		}
		// 招待を受けている側 (User2) のみ対象。自分が招待側 (User1) のゲームは
		// invitations UI の文脈的に表示しない。
		if g.User2ID != user.ID {
			continue
		}
		if g.User1 == nil {
			continue
		}
		if _, dup := seen[g.User1.ID]; dup {
			continue
		}
		seen[g.User1.ID] = struct{}{}
		inviters = append(inviters, entity.PackUserLite(g.User1))
	}
	return c.JSON(http.StatusOK, inviters)
}

// ShowGame handles POST /api/reversi/show-game.
func (h *Handler) ShowGame(c echo.Context) error {
	var req struct {
		GameID string `json:"gameId"`
	}
	if err := c.Bind(&req); err != nil || req.GameID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "gameId is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	game, err := h.repo.FindByID(req.GameID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_GAME", "No such game.", "d8a95858-973b-4f3b-8592-fcf2eb4dd044"))
	}
	// ユーザーが invitations UI からではなく「自分の対局」一覧などから
	// pending game に直接入った場合、/match が呼ばれないままなので相手側が
	// ゲームを作成できない (Join が飛ばない)。viewer が User2 (招待された
	// 側) で game が pre-start、かつ User1 がリモートなら Join を自動配信
	// する (#417 P1 UDS 検証で発覚)。deliver は idempotent。
	viewer := middleware.GetUser(c)
	if viewer != nil && !game.IsStarted && !game.IsEnded && game.User2ID == viewer.ID {
		h.sendJoinForAcceptedInvite(c, viewer, game)
	}
	return c.JSON(http.StatusOK, packGame(game, h.idGen))
}

// Match handles POST /api/reversi/match — create or join a game.
// userId は本家互換で local user id を受け付けるが、CherryPick 拡張として
// `@user` / `@user@host` 形式 (acct) も受け入れる。これは vanilla Misskey
// フロントエンドが「対戦相手選択画面」を持たず、リモートユーザーを選択で
// きない制約を緩和するための backend-side workaround。
func (h *Handler) Match(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		UserID string `json:"userId"`
	}
	_ = c.Bind(&req)

	// acct 形式 (@user / @user@host) を local user id に解決する
	if strings.HasPrefix(req.UserID, "@") && h.userRepo != nil {
		resolved, err := h.resolveAcct(req.UserID)
		if err != nil {
			return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_USER", "No such user.", "6cc579cc-885d-43d8-95c2-b8c7fc963280"))
		}
		req.UserID = resolved
	}

	// 既に相手から招待を受けている pending game があれば、それを accept 扱いで
	// 再利用する (#417 P1)。CherryPick 本家の matchSpecificUser と同じ動作:
	// inbound Invite 経由で作成された reversi_game 行 (User1=相手, User2=自分)
	// を流用し、相手に Join を返すだけにする。これをやらないと /match が毎回
	// 新しいゲーム + 新しい session_id を作って二重招待になり、state 伝播が
	// 噛み合わなくなる。
	if req.UserID != "" {
		if existing := h.findPendingInvitationFrom(user.ID, req.UserID); existing != nil {
			h.sendJoinForAcceptedInvite(c, user, existing)
			return c.JSON(http.StatusOK, packGame(existing, h.idGen))
		}
	}

	// target user を一度だけ引いて pre-check と deliver 両方で使い回す
	// (#417 P3 Devin review: 元実装では pre-check と delivery で 2 回
	// FindByID していた)。
	var targetUser *model.User
	if req.UserID != "" && h.userRepo != nil {
		if u, err := h.userRepo.FindByID(req.UserID); err == nil {
			targetUser = u
		}
	}

	// 新規招待で remote target の場合は reversiVersion 互換性を先に確認する
	// (#417 P3)。非対応ホストへの招待は silent に握りつぶされるだけで UI が
	// 「待機中」のまま動かないので、ゲーム行作成前に 400 エラーで弾く。
	if targetUser != nil && h.fedChecker != nil && targetUser.Host != nil && *targetUser.Host != "" {
		if !h.fedChecker.Available(c.Request().Context(), *targetUser.Host) {
			return c.JSON(http.StatusBadRequest, apierr.Error(
				"NO_REVERSI_FEDERATION",
				"The target user's server does not support reversi federation.",
				"3c9d76c8-8d40-4f6e-9bea-e4e57ae02fed"))
		}
	}

	now := time.Now()
	game := &model.ReversiGame{
		ID:                   h.idGen.Generate(now),
		User1ID:              user.ID,
		User2ID:              req.UserID,
		Map:                  defaultMap,
		BW:                   "random",
		TimeLimitForEachTurn: 90,
		Logs:                 datatypes.JSON("[]"),
	}
	if req.UserID == "" {
		// ランダムマッチ — user2IDを空にしてマッチ待ち
		game.User2ID = user.ID // 自分vs自分 (仮置き、実際はマッチング待ち)
	}

	if err := h.repo.Create(game); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	// ターゲット種別によって挙動分岐:
	//   - remote: federation session を Redis に保存 + Invite 送信
	//            (Host != nil かつ URI != nil の両方を要求するのは remote
	//             branch で AP Invite の targetURI として URI が必要なため)
	//   - local : 招待された側の reversi stream に `invited` イベントを push
	//            (CherryPick 互換。フロントが polling 無しで招待を検知できる)
	//   - host はあるが URI が nil の degenerate state は理論上起こらない
	//     (resolver が両方セットする) のでここで local 扱いに落ちても
	//     Redis publish は subscriber 無しで no-op となり実害なし。
	if targetUser != nil {
		if targetUser.Host != nil && targetUser.URI != nil {
			// remote: AP Invite を配信 (reversiVersion 互換性は
			// ゲーム行作成前の pre-check 済み)。
			if h.deliverer != nil && h.fedCache != nil {
				sessionID := h.idGen.Generate(now) + "-fed"
				h.fedCache.Set(c.Request().Context(), sessionID, game.ID)
				invite := corereversi.RenderInvite(h.baseURL, sessionID,
					h.baseURL+"/users/"+user.ID, *targetUser.URI, now.UTC().Format(time.RFC3339))
				if body, jerr := json.Marshal(invite); jerr == nil {
					_ = h.deliverer.DeliverToUser(user.ID, targetUser, body)
				}
			}
		} else if h.streamPub != nil {
			// local: stream に invited イベントを push (#417 P2)
			h.streamPub.PublishInvited(targetUser.ID, user)
		}
	}

	return c.JSON(http.StatusOK, packGame(game, h.idGen))
}

// findPendingInvitationFrom scans viewer's recent reversi_game rows and
// returns the pending game where User1=inviter, User2=viewer, not started / ended.
// inbound Invite によって作られた受信待ち招待を「ゲーム成立」として消費する
// ために使う。corereversi.FindPendingInvitation に共有実装。
func (h *Handler) findPendingInvitationFrom(viewerID, inviterID string) *model.ReversiGame {
	return corereversi.FindPendingInvitation(h.repo, viewerID, inviterID)
}

// sendJoinForAcceptedInvite delivers a Join activity to the remote inviter so
// both sides agree the match has started. local-only の招待の場合 (User1 が
// ローカル) は federation 不要なので何もしない。
func (h *Handler) sendJoinForAcceptedInvite(c echo.Context, accepter *model.User, game *model.ReversiGame) {
	if h.userRepo == nil || h.deliverer == nil || h.fedCache == nil {
		return
	}
	inviter, err := h.userRepo.FindByID(game.User1ID)
	if err != nil || inviter == nil || inviter.Host == nil || inviter.URI == nil {
		return
	}
	ctx := c.Request().Context()
	sessionID, ok := h.fedCache.GetSessionByGame(ctx, game.ID)
	if !ok {
		return
	}
	// show-game 経由で User2 が pending game を表示するたびに呼ばれる
	// 経路があるため、session ごとに 1 回だけ送るよう guard する
	// (#417 Devin review)。相手側 (CherryPick) でも zset idempotent なので
	// 重複しても実害は無いが、無駄な deliver job を抑える。
	if h.fedCache.IsJoinSent(ctx, sessionID) {
		return
	}
	join := corereversi.RenderJoin(h.baseURL, sessionID,
		h.baseURL+"/users/"+accepter.ID, *inviter.URI, time.Now().UTC().Format(time.RFC3339))
	body, err := json.Marshal(join)
	if err != nil {
		return
	}
	if err := h.deliverer.DeliverToUser(accepter.ID, inviter, body); err != nil {
		return
	}
	h.fedCache.MarkJoinSent(ctx, sessionID)
}

// CancelMatch handles POST /api/reversi/cancel-match.
// Service.CancelGame 経由で: Leave をリモート相手に配信 + canceled
// イベントを WebSocket に publish + fedCache の session mapping も
// 片付く。svc 未配線な旧テスト互換のため repo.Delete フォールバックも残す。
func (h *Handler) CancelMatch(c echo.Context) error {
	user := middleware.GetUser(c)
	ctx := c.Request().Context()
	games, _ := h.repo.ListByUser(user.ID, 10)
	for _, g := range games {
		if g.IsStarted || g.IsEnded {
			continue
		}
		if h.svc != nil {
			// ListByUser snapshot 後に相手が ready / start させた場合、
			// ErrAlreadyStarted で失敗する。その場合は進行中のゲームを
			// 潰さないよう fedCache の掃除もせず skip する
			// (#417 Devin review: TOCTOU)。ErrAlreadyStarted 以外の
			// 想定外エラー (DB障害等) は observability のため WARN する。
			if err := h.svc.CancelGame(ctx, g.ID, user.ID); err != nil {
				if !errors.Is(err, corereversi.ErrAlreadyStarted) {
					slog.Warn("reversi cancel-match: cancel failed",
						"gameId", g.ID, "userId", user.ID, "err", err)
				}
				continue
			}
		} else {
			_ = h.repo.Delete(g.ID)
		}
		// fedCache 片付けは Service.CancelGame が既に実行済み (#417 Devin
		// review で全終了経路に統一)。
	}
	return c.NoContent(http.StatusNoContent)
}

// Surrender handles POST /api/reversi/surrender.
// 対局中 (IsStarted かつ not IsEnded) のみ許可する。service.Surrender を経由
// することで WebSocket チャネルに `ended` イベントを publish するので、両
// プレイヤーのクライアントが即座に終局状態を検出できる。
func (h *Handler) Surrender(c echo.Context) error {
	user := middleware.GetUser(c)
	var req struct {
		GameID string `json:"gameId"`
	}
	if err := c.Bind(&req); err != nil || req.GameID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "gameId is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}

	// federation Leave を送るために winner (= 相手) を先に引いておく。
	// service.Surrender は repo 越しに game を読むが、federation session の
	// lookup は handler のタイミングで必要なので preload する。
	game, err := h.repo.FindByID(req.GameID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_GAME", "No such game.", "d8a95858-973b-4f3b-8592-fcf2eb4dd044"))
	}
	if game.User1ID != user.ID && game.User2ID != user.ID {
		return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "Access denied.", "1fb7cb09-d46a-4fff-b8df-057708cce513"))
	}

	// service が注入されていればそちらを経由する (本来のパス)。
	// フォールバックは従来の repo 直接操作 (service 未注入の古いテスト互換)。
	if h.svc != nil {
		if err := h.svc.Surrender(c.Request().Context(), req.GameID, user.ID); err != nil {
			return surrenderErrorResponse(c, err)
		}
	} else {
		// svc 未配線 path は legacy test 互換。winnerID もここで計算する。
		var winnerID string
		if game.User1ID == user.ID {
			winnerID = game.User2ID
		} else {
			winnerID = game.User1ID
		}
		now := time.Now()
		game.IsEnded = true
		game.EndedAt = &now
		game.WinnerID = &winnerID
		game.SurrenderedUserID = &user.ID
		_ = h.repo.Update(game)
	}

	// Leave 配信 + fedCache cleanup は Service.Surrender が実行済み
	// (#417 Devin review で全終了経路を Service 側に統一)。
	return c.NoContent(http.StatusNoContent)
}

// surrenderErrorResponse maps core/reversi service errors to Misskey-
// compatible HTTP error bodies. Unknown errors fall back to 500.
func surrenderErrorResponse(c echo.Context, err error) error {
	switch {
	case errors.Is(err, corereversi.ErrGameNotFound):
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_GAME", "No such game.", "d8a95858-973b-4f3b-8592-fcf2eb4dd044"))
	case errors.Is(err, corereversi.ErrNotPlayer):
		return c.JSON(http.StatusForbidden, apierr.Error("ACCESS_DENIED", "Access denied.", "1fb7cb09-d46a-4fff-b8df-057708cce513"))
	case errors.Is(err, corereversi.ErrAlreadyEnded):
		return c.JSON(http.StatusBadRequest, apierr.Error("ALREADY_ENDED", "Game has already ended.", "2a3a7f72-bc06-4f4e-9f7c-b7f8d4f6a09e"))
	case errors.Is(err, corereversi.ErrNotStarted):
		return c.JSON(http.StatusBadRequest, apierr.Error("NOT_STARTED", "Game has not started yet.", "ac4bb45f-ea81-44d3-a5b3-fe5f30be2c8d"))
	}
	return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
}

// Verify handles POST /api/reversi/verify — verify game integrity.
// CherryPick 互換: クライアントが送ってくる `crc32` をサーバ側で再計算した
// ものと比較し、不一致なら `{desynced: true, game}` を返してフロント側で
// restoreGame を走らせる。一致なら `{desynced: false}` のみ。
func (h *Handler) Verify(c echo.Context) error {
	var req struct {
		GameID string `json:"gameId"`
		CRC32  string `json:"crc32"`
	}
	if err := c.Bind(&req); err != nil || req.GameID == "" {
		return c.JSON(http.StatusBadRequest, apierr.Error("INVALID_PARAM", "gameId is required.", "ed1d7571-a3ac-4370-899c-0dbe5e230cc8"))
	}
	game, err := h.repo.FindByID(req.GameID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.Error("NO_SUCH_GAME", "No such game.", "d8a95858-973b-4f3b-8592-fcf2eb4dd044"))
	}

	// ログを再生して engine CRC を算出し、client が送ってきた crc32 と
	// 比較する。CRC が合わなければ desync 判定。client が crc32 を送って
	// こないケースは比較不可なので常に desynced=false。ログ parse は
	// EngineFromGame を再利用して二重実装を避ける (#417 Devin review)。
	g, err := corereversi.EngineFromGame(game)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.Error("INTERNAL_ERROR", "Internal error.", "5d37dbcb-891e-41ca-a3d6-e690c97775ac"))
	}

	serverCRC := strconv.FormatUint(uint64(g.CalcCRC32()), 10)
	desynced := req.CRC32 != "" && req.CRC32 != serverCRC
	resp := map[string]any{"desynced": desynced}
	if desynced {
		resp["game"] = packGame(game, h.idGen)
	}
	return c.JSON(http.StatusOK, resp)
}
