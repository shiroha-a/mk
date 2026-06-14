package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/pprof"
	urlpkg "net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/activitypub"
	apiadmin "github.com/shiroha-a/mk/internal/api/admin"
	apiannouncements "github.com/shiroha-a/mk/internal/api/announcements"
	"github.com/shiroha-a/mk/internal/api/antennas"
	"github.com/shiroha-a/mk/internal/api/ap"
	"github.com/shiroha-a/mk/internal/api/apierr"
	apiapp "github.com/shiroha-a/mk/internal/api/app"
	apiauth "github.com/shiroha-a/mk/internal/api/auth"
	"github.com/shiroha-a/mk/internal/api/blocking"
	apibubblegame "github.com/shiroha-a/mk/internal/api/bubblegame"
	apichannels "github.com/shiroha-a/mk/internal/api/channels"
	apicharts "github.com/shiroha-a/mk/internal/api/charts"
	apichat "github.com/shiroha-a/mk/internal/api/chat"
	"github.com/shiroha-a/mk/internal/api/clips"
	"github.com/shiroha-a/mk/internal/api/drive"
	apiemojis "github.com/shiroha-a/mk/internal/api/emojis"
	apifederation "github.com/shiroha-a/mk/internal/api/federation"
	apifetchexternal "github.com/shiroha-a/mk/internal/api/fetchexternal"
	apifetchrss "github.com/shiroha-a/mk/internal/api/fetchrss"
	apiflash "github.com/shiroha-a/mk/internal/api/flash"
	"github.com/shiroha-a/mk/internal/api/following"
	apigallery "github.com/shiroha-a/mk/internal/api/gallery"
	apihashtags "github.com/shiroha-a/mk/internal/api/hashtags"
	"github.com/shiroha-a/mk/internal/api/i"
	"github.com/shiroha-a/mk/internal/api/inbox"
	apiinvite "github.com/shiroha-a/mk/internal/api/invite"
	"github.com/shiroha-a/mk/internal/api/meta"
	"github.com/shiroha-a/mk/internal/api/mute"
	"github.com/shiroha-a/mk/internal/api/nodeinfo"
	"github.com/shiroha-a/mk/internal/api/notehide"
	"github.com/shiroha-a/mk/internal/api/notes"
	"github.com/shiroha-a/mk/internal/api/notifications"
	"github.com/shiroha-a/mk/internal/api/pages"
	apiproxy "github.com/shiroha-a/mk/internal/api/proxy"
	"github.com/shiroha-a/mk/internal/api/renotemute"
	apiresetpassword "github.com/shiroha-a/mk/internal/api/resetpassword"
	apireversi "github.com/shiroha-a/mk/internal/api/reversi"
	apiroles "github.com/shiroha-a/mk/internal/api/roles"
	apisignin "github.com/shiroha-a/mk/internal/api/signin"
	apisignup "github.com/shiroha-a/mk/internal/api/signup"
	"github.com/shiroha-a/mk/internal/api/streaming"
	apisw "github.com/shiroha-a/mk/internal/api/sw"
	apitest "github.com/shiroha-a/mk/internal/api/test"
	apiurl "github.com/shiroha-a/mk/internal/api/url"
	apiuserlists "github.com/shiroha-a/mk/internal/api/userlists"
	"github.com/shiroha-a/mk/internal/api/users"
	apiwebhooks "github.com/shiroha-a/mk/internal/api/webhooks"
	"github.com/shiroha-a/mk/internal/api/wellknown"
	coreabuse "github.com/shiroha-a/mk/internal/core/abuse"
	coreannouncement "github.com/shiroha-a/mk/internal/core/announcement"
	coreantenna "github.com/shiroha-a/mk/internal/core/antenna"
	"github.com/shiroha-a/mk/internal/core/avatardecoration"
	coreblocking "github.com/shiroha-a/mk/internal/core/blocking"
	corecaptcha "github.com/shiroha-a/mk/internal/core/captcha"
	corechannel "github.com/shiroha-a/mk/internal/core/channel"
	"github.com/shiroha-a/mk/internal/core/chart"
	"github.com/shiroha-a/mk/internal/core/chart/charthook"
	corechat "github.com/shiroha-a/mk/internal/core/chat"
	coreclip "github.com/shiroha-a/mk/internal/core/clip"
	coredrive "github.com/shiroha-a/mk/internal/core/drive"
	coreemail "github.com/shiroha-a/mk/internal/core/email"
	coreemojiimport "github.com/shiroha-a/mk/internal/core/emojiimport"
	"github.com/shiroha-a/mk/internal/core/event"
	corefeatured "github.com/shiroha-a/mk/internal/core/featured"
	corefederation "github.com/shiroha-a/mk/internal/core/federation"
	coreflash "github.com/shiroha-a/mk/internal/core/flash"
	corefollowing "github.com/shiroha-a/mk/internal/core/following"
	corehashtag "github.com/shiroha-a/mk/internal/core/hashtag"
	coreinstance "github.com/shiroha-a/mk/internal/core/instance"
	coremediaproxy "github.com/shiroha-a/mk/internal/core/mediaproxy"
	coremodlog "github.com/shiroha-a/mk/internal/core/moderationlog"
	coremoderatoractivity "github.com/shiroha-a/mk/internal/core/moderatoractivity"
	coremove "github.com/shiroha-a/mk/internal/core/move"
	coremuting "github.com/shiroha-a/mk/internal/core/muting"
	corenote "github.com/shiroha-a/mk/internal/core/note"
	corenotification "github.com/shiroha-a/mk/internal/core/notification"
	corepage "github.com/shiroha-a/mk/internal/core/page"
	corepoll "github.com/shiroha-a/mk/internal/core/poll"
	corereaction "github.com/shiroha-a/mk/internal/core/reaction"
	corerelay "github.com/shiroha-a/mk/internal/core/relay"
	coreretention "github.com/shiroha-a/mk/internal/core/retention"
	corereversi "github.com/shiroha-a/mk/internal/core/reversi"
	corerole "github.com/shiroha-a/mk/internal/core/role"
	coresearch "github.com/shiroha-a/mk/internal/core/search"
	"github.com/shiroha-a/mk/internal/core/serverstats"
	coresignup "github.com/shiroha-a/mk/internal/core/signup"
	coresystemaccount "github.com/shiroha-a/mk/internal/core/systemaccount"
	coretimeline "github.com/shiroha-a/mk/internal/core/timeline"
	coretransfer "github.com/shiroha-a/mk/internal/core/transfer"
	coretranslate "github.com/shiroha-a/mk/internal/core/translate"
	coretwofactor "github.com/shiroha-a/mk/internal/core/twofactor"
	coreurlpreview "github.com/shiroha-a/mk/internal/core/urlpreview"
	coreuser "github.com/shiroha-a/mk/internal/core/user"
	coreuserlist "github.com/shiroha-a/mk/internal/core/userlist"
	corewebhook "github.com/shiroha-a/mk/internal/core/webhook"
	corewebpush "github.com/shiroha-a/mk/internal/core/webpush"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/frontendutil"
	"github.com/shiroha-a/mk/internal/misc/id"
	miscsmtp "github.com/shiroha-a/mk/internal/misc/smtp"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/processors"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/server/middleware"
	"github.com/shiroha-a/mk/internal/stream"
	"github.com/shiroha-a/mk/internal/stream/channels"
	"log/slog"
)

func (s *Server) setupRoutes() {
	idGen, err := id.NewGenerator(s.config.ID)
	if err != nil {
		idGen, _ = id.NewGenerator("aidx")
	}

	// Repositories
	// userRepo は server.New で構築した CachedUserRepository を再利用する。
	// auth middleware と service 層が同じ cache を共有することで mutation
	// 時の invalidate が両方に反映される (#300 3-3)。
	userRepo := s.userRepo
	noteRepo := repository.NewNoteRepository(s.db)
	metaRepo := repository.NewCachedMetaRepository(repository.NewMetaRepository(s.db))
	// Seed the singleton meta row on first boot so that fresh installs
	// can run /api/admin/accounts/create (initial setup) without tripping
	// over a missing meta row.
	if err := metaRepo.EnsureInitial(idGen.Generate(time.Now())); err != nil {
		slog.Error("failed to ensure initial meta row", "err", err)
	}
	pollRepo := repository.NewPollRepository(s.db)
	followingRepo := repository.NewFollowingRepository(s.db)
	followRequestRepo := repository.NewFollowRequestRepository(s.db)
	piningRepo := repository.NewUserNotePiningRepository(s.db)
	reactionRepo := repository.NewNoteReactionRepository(s.db)
	// emoji の ListLocal はタイムライン描画ごとに毎回フルスキャンしていた
	// hot path だが、絵文字テーブルの mutation は admin 経由のみで頻度が低い。
	// 5 分 TTL の in-memory cache + mutation 時 invalidate で DB 負荷を消す
	// (#300 3-6)。
	emojiRepo := repository.NewCachedEmojiRepository(repository.NewEmojiRepository(s.db))
	blockingRepo := repository.NewBlockingRepository(s.db)
	mutingRepo := repository.NewMutingRepository(s.db)
	renoteMutingRepo := repository.NewRenoteMutingRepository(s.db)
	pollVoteRepo := repository.NewPollVoteRepository(s.db)
	driveFileRepo := repository.NewDriveFileRepository(s.db)
	driveFolderRepo := repository.NewDriveFolderRepository(s.db)
	keypairRepo := repository.NewUserKeypairRepository(s.db)
	keypairExtraRepo := repository.NewUserKeypairExtraRepository(s.db)
	instanceRepo := repository.NewInstanceRepository(s.db)
	// instance.{followersCount,followingCount} は following service の
	// adjustInstanceCountsForFollowing (Follow/Unfollow/AcceptRequest) と
	// blocking service の auto-unfollow で incremental に維持される
	// (admin/overview の federation pie chart の data source)。起動時の
	// backfill は、再起動時点での following テーブルとの整合性回復 +
	// direct DB 改変や過去の counter drift への安全網として残す (#421)。
	// 失敗は警告だけで起動継続。
	if err := instanceRepo.RecomputeFollowCounts(); err != nil {
		slog.Warn("instance follow-counts recompute failed", "err", err)
	}
	channelRepo := repository.NewChannelRepository(s.db)
	channelFollowingRepo := repository.NewChannelFollowingRepository(s.db)
	antennaRepo := repository.NewAntennaRepository(s.db)
	clipRepo := repository.NewClipRepository(s.db)
	clipNoteRepo := repository.NewClipNoteRepository(s.db)
	pageRepo := repository.NewPageRepository(s.db)
	pageLikeRepo := repository.NewPageLikeRepository(s.db)
	flashRepo := repository.NewFlashRepository(s.db)
	flashLikeRepo := repository.NewFlashLikeRepository(s.db)
	roleRepo := repository.NewRoleRepository(s.db)
	roleAssignmentRepo := repository.NewRoleAssignmentRepository(s.db)
	swSubRepo := repository.NewSwSubscriptionRepository(s.db)
	noteFavoriteRepo := repository.NewNoteFavoriteRepository(s.db)
	noteThreadMutingRepo := repository.NewNoteThreadMutingRepository(s.db)
	userListRepo := repository.NewUserListRepository(s.db)
	webhookRepo := repository.NewWebhookRepository(s.db)
	systemWebhookRepo := repository.NewSystemWebhookRepository(s.db)
	reversiRepo := repository.NewReversiRepository(s.db)
	// userIPRepo は signin handler (Phase 6) と clean cron processor (#1563)
	// の双方で使うため、他 repo と同じ早い段階で構築する。
	userIPRepo := repository.NewUserIPRepository(s.db)
	chatRepo := repository.NewChatRepository(s.db)
	channelFavoriteRepo := repository.NewChannelFavoriteRepository(s.db)
	channelMutingRepo := repository.NewChannelMutingRepository(s.db)
	clipFavoriteRepo := repository.NewClipFavoriteRepository(s.db)
	userListFavoriteRepo := repository.NewUserListFavoriteRepository(s.db)
	retentionRepo := repository.NewRetentionAggregationRepository(s.db)
	promoReadRepo := repository.NewPromoReadRepository(s.db)
	hashtagRepo := repository.NewHashtagRepository(s.db)

	// Core services
	roleService := corerole.NewService(roleRepo, roleAssignmentRepo, metaRepo, idGen)
	// drop-in 互換 (#785): Misskey TS は signup root user を user.isRoot=true で
	// マークし meta.rootUserId は set しない。TS DB を引き継いだ mk-go では
	// meta.rootUserId=nil となり admin paths で 403 が返る regression を防ぐ
	// ため、role.Service に userRepo を渡して user.isRoot fallback を有効化する。
	roleService.SetUserRepo(userRepo)
	signupService := coresignup.NewService(userRepo, metaRepo, idGen)
	// ActivityPub 配信のためにローカルユーザーは RSA 鍵対を必要とする。
	signupService.SetKeypairRepo(keypairRepo)
	// FEP-521a Multikey 対応で Ed25519 鍵対も併発行 (#1067 / #1068)。
	signupService.SetKeypairExtraRepo(keypairExtraRepo)
	noteCreateService := corenote.NewCreateService(noteRepo, pollRepo, idGen, followingRepo)
	// Phase 7-1 follow-up (#254): 本家互換の残存 error 検出ロジック用依存を注入。
	// Setter経由なのでテストでは任意に省略可能。既存の同repository変数を再利用。
	noteCreateService.SetBlockingRepo(blockingRepo)
	noteCreateService.SetDriveFileRepo(driveFileRepo)
	noteCreateService.SetMetaRepo(metaRepo)
	noteCreateService.SetChannelRepo(channelRepo)
	// canPublicNote=false の user の public note を home に降格させる
	// silencing 機構 (#1024)。
	noteCreateService.SetSilencingProvider(roleService)
	noteDeleteService := corenote.NewDeleteService(noteRepo)
	noteDeleteService.SetUserRepo(userRepo) // #1538: resolve author for moderator delete
	noteQueryService := corenote.NewQueryService(noteRepo, followingRepo)
	noteQueryService.SetFavoriteRepo(noteFavoriteRepo)
	noteQueryService.SetThreadMutingRepo(noteThreadMutingRepo)
	userService := coreuser.NewService(userRepo, noteRepo, piningRepo, idGen)
	// PinNote の上限を role policy `pinLimit` で override 可能にする (#1029)。
	userService.SetRolePolicyProvider(roleService)
	// /api/i/update の avatarId / bannerId 経路 (#467) で drive_file の
	// 所有権検証 + URL コピーが必要なため、driveFileRepo を配線する。
	userService.SetDriveFileRepository(driveFileRepo)
	// users/search-by-username-and-host で host=<self-hostname> 入力時に
	// local 限定にする upstream 互換 remap (#1064) のため、自 hostname を
	// Service に渡す。空文字なら remap は "." 一致のみになる。
	userService.SetSelfHostname(s.config.Hostname)
	followingService := corefollowing.NewService(userRepo, followingRepo, followRequestRepo, idGen)
	// remote instance の followersCount / followingCount を Follow/Unfollow 時に
	// incremental 更新する (#596)。未配線でも本機能には影響しないが、admin
	// dashboard の federation pie chart が起動直後以外で 0 に偏る。
	followingService.SetInstanceRepo(instanceRepo)

	// Timeline services (Redis-backed fanout)
	// keyPrefix で TS 本家と同じ `<host>:list:*` 名前空間に揃える (#362)。
	fanoutTimelineService := coretimeline.NewFanoutTimelineService(
		s.redis.Timelines, idGen, s.config.RedisForTimelines.KeyPrefix())
	timelineService := coretimeline.NewService(fanoutTimelineService, noteRepo, followingRepo)
	timelineFanoutHook := coretimeline.NewFanoutHook(fanoutTimelineService, followingRepo)
	// Phase DB-compat (#51): meta から timeline cache cap を動的に読む。
	// 4 つのカラム (perLocal / perRemote / perHome / perList) が反映される。
	timelineFanoutHook.SetCacheLimitsProvider(coretimeline.NewMetaRepoCacheLimits(metaRepo))
	timelineFanoutHook.SetUserListRepo(userListRepo)
	timelineFanoutHook.SetUserRolesLookup(roleService) // #1549: roleTimeline fanout
	// #1686: channel note を channel follower の home timeline へ fanout する。
	timelineFanoutHook.SetChannelFollowerRepo(channelFollowingRepo)
	noteCreateService.SetFanoutHook(timelineFanoutHook)
	// #379: Delete (inbound activity / local notes/delete どちらも) で
	// Redis fanout timelines から note ID を LREM するための hook。
	noteDeleteService.SetTimelineHook(timelineFanoutHook)

	// Channels (Phase 4.2)
	channelService := corechannel.NewService(channelRepo, channelFollowingRepo, noteRepo, idGen)
	// channels/update の moderator bypass (#1540): owner でなくても moderator
	// なら他人の channel を編集できる upstream 互換のため roleService を inject。
	channelService.SetModeratorChecker(roleService)
	// Phase 7-2 follow-up (#271): channel note 着信時に follower ごとの
	// unread row を作成して /api/i の hasUnreadChannel に反映する。
	channelNoteUnreadRepo := repository.NewChannelNoteUnreadRepository(s.db)
	channelService.SetUnreadRepo(channelNoteUnreadRepo)
	noteUnreadRepo := repository.NewNoteUnreadRepository(s.db)
	noteCreateService.SetChannelHook(corechannel.NewNoteCreateHook(channelService))

	// Antennas (Phase 4.3)
	antennaService := coreantenna.NewService(antennaRepo, userRepo, s.redis.Default, idGen)
	antennaService.SetFollowingRepo(followingRepo)
	antennaService.SetUserListRepo(userListRepo)
	antennaService.SetRolePolicyProvider(roleService) // #1029: antennaLimit
	// Phase 7-2 follow-up (#271): antenna 着信時に所有者の unread row を
	// 作成して /api/i の hasUnreadAntenna に反映する。
	antennaNoteUnreadRepo := repository.NewAntennaNoteUnreadRepository(s.db)
	antennaService.SetUnreadRepo(antennaNoteUnreadRepo)
	noteCreateService.SetAntennaHook(coreantenna.NewNoteCreateHook(antennaService))

	// Clips (Phase 4.4)
	clipService := coreclip.NewService(clipRepo, clipNoteRepo, noteRepo, idGen)
	clipService.SetRolePolicyProvider(roleService) // #1029: clipLimit / noteEachClipsLimit

	// Pages / Flash (Phase 4.5)
	pageService := corepage.NewService(pageRepo, pageLikeRepo, idGen)
	flashService := coreflash.NewService(flashRepo, flashLikeRepo, idGen)

	// featured engagement ランキング (#1687)。reaction / renote の hook が
	// global / in-channel / per-user の Redis windowed ZSET を更新し、
	// notes/featured・users/featured-notes が読む。upstream と同じ default Redis。
	featuredService := corefeatured.NewService(s.redis.Default)
	noteCreateService.SetFeaturedRanking(featuredService)

	// Reactions
	reactionService := corereaction.NewService(noteRepo, reactionRepo, emojiRepo, followingRepo, idGen)
	reactionService.SetFeaturedRanking(featuredService)

	// Reactions buffering (issue #57): meta.enableReactionsBuffering が true なら
	// Redis にバッファし、30秒ごとに DB へ flush する。
	var reactionCountWriter corereaction.ReactionCountWriter
	if bufMeta, err := metaRepo.Fetch(); err == nil && bufMeta.EnableReactionsBuffering {
		reactionCountWriter = corereaction.NewBufferedWriter(s.redis.Default, noteRepo)
		reactionService.SetCountWriter(reactionCountWriter)
	} else {
		reactionCountWriter = reactionService.CountWriter()
	}

	// Notifications (Redis Streams)
	// keyPrefix で TS 本家と同じ `<host>:notificationTimeline:*` 等に揃える (#362)。
	notificationService := corenotification.NewService(s.redis.Default, idGen, s.config.Redis.KeyPrefix())
	notificationService.SetNoteUnreadRepo(noteUnreadRepo)
	notificationHook := corenotification.NewHook(notificationService, userRepo)
	notificationHook.SetNoteUnreadRepo(noteUnreadRepo)
	// note 通知 (notify='normal' フォロワーへの投稿通知、#1559) の fan-out 依存。
	notificationHook.SetNoteNotifyRepos(followingRepo, renoteMutingRepo)
	noteCreateService.SetNotificationHook(notificationHook)
	noteCreateService.SetUserRepo(userRepo)
	// roleAssigned 通知 (#1559): local public role 割当時に発火し、entity 側で
	// read 時に packed role へ解決する。lookup は role 削除済なら (nil,false)。
	roleService.SetRoleAssignNotifier(notificationHook)
	roleNotifLookup := func(roleID string) (map[string]any, bool) {
		r, err := roleRepo.FindByID(roleID)
		if err != nil || r == nil {
			return nil, false
		}
		return entity.PackRole(r, roleService.CountAssignedUsers(roleID), idGen, corerole.DefaultPolicies()), true
	}
	followingService.SetNotificationHook(notificationHook)
	reactionService.SetNotificationHook(notificationHook)

	// Web Push (Phase 9.3): 通知作成後にサブスクライバーへpush配信する。
	webPushService := corewebpush.NewService(s.queueClient)
	notificationHook.SetWebPushPublisher(webPushService)
	notificationHook.SetPackers(
		corewebpush.NewUserRepoPacker(userRepo),
		corewebpush.NewNoteRepoPacker(noteRepo, idGen, followingRepo),
	)
	webPushCache := corewebpush.NewSubscriptionCache(swSubRepo, s.redis.Default)
	// Web Push delivery: webpush-go の HTTPClient field に SSRF-safe client
	// を注入して FCM / Mozilla / Apple endpoint への配送も outbound 政策
	// (forward proxy / outgoingAddress 等) に従わせる (#638)。
	webPushSender := processors.LibraryWebPushSender{HTTPClient: s.outboundClient(30 * time.Second)}
	webPushProcessor := processors.NewWebPushProcessor(
		webPushCache, swSubRepo, metaRepo, webPushSender, s.config.URL,
	)
	s.queueServer.Handle(queue.TaskTypeWebPush, webPushProcessor.Handle)

	// Webhook (Phase 9.5): ユーザー/システムwebhookの配信を非同期処理する。
	webhookService := corewebhook.NewService(s.queueClient, webhookRepo, systemWebhookRepo, s.config.URL)
	noteCreateService.SetWebhookHook(corewebhook.NewNoteCreateHook(webhookService, idGen))
	reactionService.SetWebhookHook(corewebhook.NewReactionCreateHook(webhookService, idGen))
	followingService.SetWebhookHook(corewebhook.NewFollowingHook(webhookService))
	signupService.SetWebhookHook(corewebhook.NewSignupHook(webhookService))
	// Webhook delivery: SSRF-safe transport + forward proxy 経由で user-supplied
	// URL に POST する (#638)。Timeout は processors 側の DefaultWebhookTimeout
	// に揃える。
	webhookProcessor := processors.NewWebhookProcessor(webhookRepo, systemWebhookRepo, s.outboundClient(processors.DefaultWebhookTimeout), s.config.Host)
	s.queueServer.Handle(queue.TaskTypeUserWebhook, webhookProcessor.HandleUser)
	s.queueServer.Handle(queue.TaskTypeSystemWebhook, webhookProcessor.HandleSystem)

	// Blocking & Muting
	blockingService := coreblocking.NewService(userRepo, blockingRepo, followingRepo, idGen)
	// Block→自動 unfollow 経路でも remote instance counter を更新 (#596)
	blockingService.SetInstanceRepo(instanceRepo)
	mutingService := coremuting.NewService(userRepo, mutingRepo, idGen)
	renoteMutingService := coremuting.NewRenoteService(userRepo, renoteMutingRepo, idGen)
	followingService.SetBlockingChecker(blockingService)
	reactionService.SetBlockingChecker(blockingService)
	notificationHook.SetMuteChecker(mutingService)

	// Polls
	pollService := corepoll.NewService(noteRepo, pollRepo, pollVoteRepo, followingRepo, idGen)
	pollService.SetNotificationHook(notificationHook)
	// note 著者にブロックされた user の投票を弾く (upstream youHaveBeenBlocked、#1538)。
	pollService.SetBlockingChecker(blockingService)
	// poll federation hook は deliverService が下で生成されるので、
	// `pollService.SetFederationHook(...)` の呼び出しは deliverService 構築後
	// (router.go:493 以降) に行う。下方の wire-up コメント参照。
	// pollEnded 通知は周期 ticker (60s) で expiresAt < NOW() AND notifiedAt
	// IS NULL を scan して著者 + ローカル投票者に発火する (#690)。Misskey TS
	// は BullMQ delayed job で実装しているが mk-go では queue infra を経由
	// せずに簡素な ticker で実現。partial index で空 scan のコストは最小。
	pollExpiryWorker := corepoll.NewExpiryWorker(pollRepo, pollVoteRepo, noteRepo, userRepo, notificationService, 60*time.Second, 100)
	pollExpiryCtx, pollExpiryCancel := context.WithCancel(context.Background())
	go pollExpiryWorker.Run(pollExpiryCtx)
	s.registerShutdownHook(func(_ context.Context) { pollExpiryCancel() })
	// pollVoted を note の stream topic に publish して subscribe 中の
	// frontend (note 詳細 / timeline) が reload なしで count を更新できる
	// ようにする (#690)。streamPubSub は下方で生成されるため遅延配線。

	// Drive storage (Phase 4.9: Meta に基づいて Local / S3 を切り替え)
	// localDriveStorage は driveStorage が S3 に切り替わっても、
	// storedInternal=true な drive_file を `/files/:accessKey` で提供する
	// fallback として常に保持する (#1414)。
	localDriveStorage := coredrive.NewLocalStorage("./drive-files", s.config.DriveURL)
	var driveStorage coredrive.Storage
	if serverMeta, err := metaRepo.Fetch(); err == nil {
		driveStorage = coredrive.NewStorageFromMeta(serverMeta, "./drive-files", s.config.DriveURL)
	} else {
		driveStorage = localDriveStorage
	}
	driveService := coredrive.NewService(driveFileRepo, driveFolderRepo, driveStorage, idGen)
	// drive/files/show は moderator なら他人 / リモートユーザー所有の file
	// も返せるようにする (upstream Misskey の roleService.isModerator 経路
	// と一致)。リモート添付メディアの詳細閲覧に必要。
	driveService.SetRoleChecker(roleService)

	// Image processing (Phase 4.8)
	imgProcessor := coredrive.NewDefaultImageProcessor()
	driveService.SetImageProcessor(imgProcessor)
	driveService.SetVideoProcessor(coredrive.NewFFmpegVideoProcessor(imgProcessor, nil))

	// Sensitive media detection (#44 / #406)。mk-go は backend に ML runtime
	// を抱えないので、operator が任意の互換 service を立てて URL を
	// `nsfwDetectorUrl` 設定に書く。URL が空なら detector=nil で自動付与は
	// 走らず、手動 isSensitive フラグのみ機能する (default)。動作モード
	// (Detection mode / Sensitivity / SetFlagAutomatically / EnableForVideos /
	// SilencedHosts) は admin の `meta` カラム経由で TS 互換に制御。
	if sensMeta, err := metaRepo.Fetch(); err == nil {
		sensCfg := coredrive.SensitiveConfig{
			Detection:            sensMeta.SensitiveMediaDetection,
			Sensitivity:          sensMeta.SensitiveMediaDetectionSensitivity,
			SetFlagAutomatically: sensMeta.SetSensitiveFlagAutomatically,
			EnableForVideos:      sensMeta.EnableSensitiveMediaDetectionForVideos,
			SilencedHosts:        sensMeta.MediaSilencedHosts,
		}
		var nsfwDetector coredrive.SensitiveDetector
		if url := s.config.NSFWDetectorURL; url != "" {
			timeout := s.config.NSFWDetectorTimeout
			if timeout <= 0 {
				timeout = coredrive.DefaultHTTPDetectorTimeout
			}
			nsfwDetector = coredrive.NewHTTPDetectorWithOptions(url, s.outboundClient(timeout), coredrive.HTTPDetectorOptions{
				AuthHeader: s.config.NSFWDetectorAuthHeader,
				Timeout:    timeout,
			})
		}
		driveService.SetSensitiveDetection(nsfwDetector, sensCfg)
	}

	// Export / Import workers (Phase 9.4): drive に保存するエクスポートと
	// drive から読み出すインポートを asynq 経由で非同期処理する。
	exporter := coretransfer.NewExporter(coretransfer.ExporterDeps{
		UserRepo:         userRepo,
		NoteRepo:         noteRepo,
		PollRepo:         pollRepo,
		FollowingRepo:    followingRepo,
		BlockingRepo:     blockingRepo,
		MutingRepo:       mutingRepo,
		NoteFavoriteRepo: noteFavoriteRepo,
		AntennaRepo:      antennaRepo,
		ClipRepo:         clipRepo,
		ClipNoteRepo:     clipNoteRepo,
		UserListRepo:     userListRepo,
		Drive:            driveService,
		Notifier:         notificationService,
		// custom-emojis export: 全 local emoji を ListLocal で列挙し、各画像を
		// SSRF-safe client で download して zip 化する (#1217)。画像取得は最大
		// 60s/個、cap 8 MiB。
		EmojiRepo:         emojiRepo,
		EmojiImageFetcher: coretransfer.NewHTTPEmojiImageFetcher(s.outboundClient(60*time.Second), s.config.UserAgent, 0),
	})
	driveReader := coretransfer.NewRepoBackedDriveReader(driveFileRepo, driveStorage)
	importer := coretransfer.NewImporter(coretransfer.ImporterDeps{
		UserRepo:     userRepo,
		UserListRepo: userListRepo,
		AntennaRepo:  antennaRepo,
		Drive:        driveReader,
		Following:    coretransfer.NewFollowingServiceAdapter(followingService),
		Blocking:     blockingService,
		Muting:       mutingService,
		Notifier:     notificationService,
		IDGen:        idGen,
		SelfHost:     s.config.Host,
	})
	exportProcessor := processors.NewExportProcessor(exporter)
	importProcessor := processors.NewImportProcessor(importer)
	s.queueServer.Handle(queue.TaskTypeExport, exportProcessor.Handle)
	s.queueServer.Handle(queue.TaskTypeImport, importProcessor.Handle)

	// Phase 9.9: admin/emoji/import-zip を非同期で処理するワーカー。
	// driveReader と driveService を再利用し ZIP 展開→Drive 保存→emoji 行作成。
	emojiImporter := coreemojiimport.NewImporter(coreemojiimport.Deps{
		UserRepo:  userRepo,
		EmojiRepo: emojiRepo,
		Drive:     driveReader,
		Uploader:  driveService,
		IDGen:     idGen,
	})
	emojiImportProcessor := processors.NewImportCustomEmojisProcessor(emojiImporter)
	s.queueServer.Handle(queue.TaskTypeImportCustomEmojis, emojiImportProcessor.Handle)

	// Search (Phase 4.6)
	// 設定に従って provider を選択する。Meilisearch が設定されていれば
	// それを使い、そうでなければ SQL ILIKE フォールバック。
	searchService := coresearch.NewService(buildSearchProvider(s.config, noteRepo, followingRepo, idGen))
	noteCreateService.SetIndexHook(coresearch.NewNoteIndexHook(searchService))
	noteDeleteService.SetIndexHook(coresearch.NewNoteIndexHook(searchService))

	// ActivityPub
	apURLs := activitypub.NewURLBuilder(s.config.URL)
	apRenderer := activitypub.NewRenderer(apURLs)
	// Mention tag を AP Note に埋め込むための resolver。
	apRenderer.SetMentionResolver(corefederation.NewUserMentionResolver(userRepo, apURLs))
	apRenderer.SetFileResolver(driveFileRepo)
	apRenderer.SetEmojiResolver(emojiRepo)
	apRenderer.SetNoteResolver(noteRepo)
	apRenderer.SetPollResolver(pollRepo)
	// config.URL は "https://example.com" 形式。ホスト部分だけ抽出して
	// apRenderer と webfinger 側で共有する (下の resolver 設定でも再利用)。
	localHost := ""
	if u, err := urlpkg.Parse(s.config.URL); err == nil {
		localHost = u.Host
		apRenderer.SetHost(localHost)
	}
	// AP outbound client: SSRF-safe transport を適用 (#323)。
	// config.AllowedPrivateNetworks で開発時の self-loop を許可できる。
	// config.Proxy / ProxyBypassHosts も #485 で wire 済み。
	apHTTPClient := s.outboundClient(30 * time.Second)
	apClient := activitypub.NewClient(apHTTPClient, s.config.UserAgent)
	// meta.allowExternalApRedirect が false なら AP fetch でのリダイレクトを拒否する。
	if m, err := metaRepo.Fetch(); err == nil && !m.AllowExternalApRedirect {
		apClient.DisableRedirect()
	}
	apFetcher := corefederation.NewAPFetcher(apClient)
	federationResolver := corefederation.NewResolver(userRepo, noteRepo, apURLs, apFetcher, idGen)
	publickeyRepo := repository.NewUserPublickeyRepository(s.db)
	federationResolver.SetPublickeyRepo(publickeyRepo)
	// FEP-521a Multikey 対応で remote actor の assertionMethod[] を parse して
	// user_publickey_extra に persist する (#1067 / #1070)。
	federationResolver.SetPublickeyExtraRepo(repository.NewUserPublickeyExtraRepository(s.db))
	federationResolver.SetPollRepo(pollRepo)
	// AP vote (Note.name + inReplyTo to a poll) を local poll service に
	// 流して投票として記録する (#690)。これがないとリモートからの投票が
	// 通常 reply として扱われ frontend に DM のように表示される。
	federationResolver.SetPollVoter(pollService)
	federationResolver.SetEmojiRepo(emojiRepo)
	federationResolver.SetDriveFileRepo(driveFileRepo)
	// AP attachment dimension probe (#461) 用 outbound HTTP client。
	// SSRF-safe transport で内部 IP / cloud metadata エンドポイントへの
	// アクセスを拒否する。timeout は単発 image fetch なので apHTTPClient
	// (30s) より短めの 10s にする。
	imageProbeClient := s.outboundClient(10 * time.Second)
	federationResolver.SetImageProbeClient(imageProbeClient)
	federationProcessor := corefederation.NewProcessor(federationResolver, followingService, reactionService, noteDeleteService, userRepo, noteRepo)
	federationProcessor.SetLocalBaseURL(s.config.URL)

	// users/show 経由で host が指定されたリモートユーザーをローカル DB に
	// キャッシュが無くても解決できるようにする (#269)。webfinger で actor URI
	// を引いてから federationResolver.ResolveActor で User row を upsert する。
	// redirect 追跡必須のため apClient とは *http.Client を分離し、
	// apClient.DisableRedirect() の影響を受けないようにする。Timeout は
	// user-facing API 経由で呼ばれるので応答性優先で 10s に設定する。
	webfingerClient := activitypub.NewWebFingerClient(s.outboundClient(10*time.Second), s.config.UserAgent)
	userService.SetRemoteUserResolver(corefederation.NewRemoteUserResolver(
		webfingerClient, federationResolver, userRepo, localHost,
	))

	// Instance management (Phase 3 Step H)
	instanceService := coreinstance.NewService(instanceRepo, metaRepo, idGen)
	federationResolver.SetInstanceTracker(instanceService)
	// #1538: reactionAcceptance gate — role-gated emoji + media-silenced host。
	// instanceService 生成後 (= 本ブロック) で配線する (reactionService は line ~281
	// で先に作られるため、media-silence checker はここで遅延注入する)。
	reactionService.SetUserRolesProvider(roleService)
	reactionService.SetMediaSilenceChecker(instanceService)
	// ホワイトリスト連合 (federation: specified) / blockedHosts に対する gate を
	// resolver の入口 (fetchActor / resolveNoteOnce / IngestNoteWithCreated) に
	// 適用する。deliver_service / inboxProcessor と同じ instanceService を共有。
	federationResolver.SetHostBlockChecker(instanceService)
	// 新規 instance row 発見時に nodeinfo を取得して metadata を更新する。
	// admin/federation/refresh-remote-instance-metadata でも同じ fetcher を
	// 再利用して on-demand で再取得する (#351 フォロー)。
	metadataFetcher := coreinstance.NewFetchMetadataService(instanceRepo, apFetcher)
	instanceService.SetMetadataFetcher(metadataFetcher)

	// inbox worker から呼ばれる MarkRequestReceived を per-host で 1s buffer
	// に集約。同一 remote host の連続 inbox 受信で 10k UPDATE が 1 UPDATE に
	// 縮退する (#569)。
	instanceTouchBuffer := coreinstance.NewTouchBuffer(instanceService, time.Second)
	instanceTouchBuffer.Start(context.Background())
	s.registerShutdownHook(func(_ context.Context) { instanceTouchBuffer.Close() })

	// AP delivery: DeliverService + フック登録 + asynq processor 登録
	deliverService := corefederation.NewDeliverService(s.queueClient, userRepo, followingRepo, keypairRepo, apURLs)
	deliverService.SetHostBlockChecker(instanceService)
	// FEP-521a Multikey 対応で recipient capable + sender Ed25519 鍵あり 経路で
	// Ed25519 sign を試行できるよう wire (#1067 / #1071)。
	deliverService.SetKeypairExtraRepo(keypairExtraRepo)
	deliverService.SetPublickeyExtraRepo(repository.NewUserPublickeyExtraRepository(s.db))
	// test (#780) で queue を bypass して同期 deliver する hook を後付けで
	// 差し替えられるよう、Server から参照を保持する。
	s.deliverSvc = deliverService

	// poll federation hook: ローカル user が remote poll に投票したら AP Note
	// (with name + inReplyTo) を author の inbox に配信する (#690)。
	pollService.SetFederationHook(corefederation.NewPollDeliveryHook(apRenderer, deliverService, userRepo, apURLs, idGen))

	// Relay 連携 (#161): relay actor system account + relay.Service。
	// relay.Service は AP delivery を必要とするので deliverService 構築の
	// 直後でセットアップする。admin/relays ハンドラへ注入し、Accept/Reject
	// inbox 処理で status を更新できるように processor にも渡す。
	sysAcctSvc := coresystemaccount.NewService(userRepo, keypairRepo, repository.NewSystemAccountRepository(s.db), idGen)
	// FEP-521a Multikey 対応で system account も Ed25519 鍵対を併発行 (#1067 / #1068)。
	sysAcctSvc.SetKeypairExtraRepo(keypairExtraRepo)
	// #1704: list に remote user を追加したとき proxy account がフォローして連合
	// 投稿を受信できるようにする enqueuer。push / create-from-public で使う。
	listProxyFollow := coreuserlist.NewProxyFollower(sysAcctSvc, s.queueClient)
	relaySvc := corerelay.NewService(repository.NewRelayRepository(s.db), sysAcctSvc, apRenderer, deliverService, idGen)

	// AP fetch のデフォルト signer に instance.actor を配線する (#419)。
	// IceShrimp.NET 等の authorized-fetch peer では未署名 GET が 401 で
	// 弾かれるため、apFetcher.FetchObject は signed GET を優先しに行く。
	// 署名失敗時は従来通り unsigned にフォールバック (deliver_service と
	// 同じ keyID 形式 = `<baseURL>/users/<id>#main-key`)。
	apFetcher.SetSigner(newInstanceActorSigner(sysAcctSvc, keypairRepo, apURLs))

	noteDeliveryHook := corefederation.NewNoteDeliveryHook(deliverService, apRenderer, apURLs, idGen, userRepo, noteRepo)
	noteDeliveryHook.SetRelayBroadcaster(relaySvc)
	noteCreateService.SetFederationHook(noteDeliveryHook)
	followingDeliveryHook := corefederation.NewFollowingDeliveryHook(deliverService, apRenderer, apURLs)
	followingService.SetFederationHook(followingDeliveryHook)
	// inbound Follow に対する Accept 返送は processor から直接呼ぶ (original
	// Follow の id を保持したまま相手に返すため、service 層を経由しない)。
	federationProcessor.SetInboundFollowAcceptor(followingDeliveryHook)
	reactionService.SetFederationHook(corefederation.NewReactionDeliveryHook(deliverService, apRenderer, apURLs, idGen, userRepo))
	// local user が remote user を (un)block した際に Block / Undo(Block) を
	// 相手 inbox へ配信する (#1560)。
	blockingService.SetFederationHook(corefederation.NewBlockingDeliveryHook(deliverService, userRepo, apRenderer))
	noteDeleteHook := corefederation.NewNoteDeleteDeliveryHook(deliverService, apRenderer, apURLs)
	noteDeleteHook.SetUserRepo(userRepo)
	noteDeleteService.SetFederationHook(noteDeleteHook)
	deliverProcessor := processors.NewDeliverProcessor(apClient)
	// 配信結果に応じて instance.isNotResponding を更新する
	deliverProcessor.SetResponseHook(instanceService)
	// suspend / block されたインスタンスへは dispatch 時に配送を止める (#1404)。
	// enqueue 時の isBlockedInbox を通り抜ける retry-backoff 中・積み残しジョブも弾く。
	deliverProcessor.SetDeliveryGate(instanceService)
	// FEP-521a Multikey 対応で host 単位の Ed25519 degrade flag を Redis に
	// 持つ (#1067 / #1071)。Ed25519 sign 失敗時 5min 同 host を RSA only に
	// 縮退する safety net。
	deliverProcessor.SetRedis(s.redis.Default)
	// deliverSuspendedSoftware: 対象インスタンスへの配送をスキップする
	if suspMeta, err := metaRepo.Fetch(); err == nil && len(suspMeta.DeliverSuspendedSoftware) > 0 {
		deliverProcessor.SetSuspendedChecker(
			corefederation.NewSuspendedChecker(suspMeta.DeliverSuspendedSoftware, instanceRepo),
		)
	}
	s.queueServer.Handle(queue.TaskTypeDeliver, deliverProcessor.Handle)

	// Inbox (#534 / #565): HTTP handler は body+signature 関連 header を
	// payload に詰めて 202 即返し。worker 側 (本 processor) で signature
	// 検証 + host block + instance touch + chart hook + Process を実行
	// する。これにより handler 同期の RSA-2048 verify (~1-2ms) が消えて
	// inbound throughput が ~2x に改善する (#565)。順序保証は各 activity
	// handler の冪等性で吸収する Misskey TS 互換戦略。
	inboxProcessor := processors.NewInboxProcessor(federationProcessor)
	inboxProcessor.SetSignatureVerifier(federationResolver)
	inboxProcessor.SetHostBlockChecker(instanceService)
	inboxProcessor.SetInstanceTracker(instanceTouchBuffer)
	// LD-Signature verifier (#1164 Phase D)。inbound activity body に signature
	// field があれば RsaSignature2017 + 2026.5.4 hardening を実行する。signature
	// 無し / verify pass の activity は素通し、verify fail は drop。
	inboxProcessor.SetLDSignatureVerifier(corefederation.NewLDSignatureVerifier(publickeyRepo))
	// chartHook は後段で SetChartHook 注入する (deliverProcessor と同じ pattern)。
	s.queueServer.Handle(queue.TaskTypeInbox, inboxProcessor.Handle)

	// Remote notes cleaning (issue #46): enqueue 契機は scheduler の cron
	// (RegisterCleanRemoteNotesJob, 毎日 04:00 UTC, TS 'cleanRemoteNotes' 相当,
	// #1563)。旧実装は 6h time.Ticker だった。Handle は常に登録し、processor が
	// cfg.Enabled で gate する (起動時 meta から構築)。
	cleanCfg := processors.CleanRemoteNotesConfig{}
	if cleanMeta, err := metaRepo.Fetch(); err == nil {
		cleanCfg = processors.CleanRemoteNotesConfig{
			Enabled:              cleanMeta.EnableRemoteNotesCleaning,
			ExpiryDays:           cleanMeta.RemoteNotesCleaningExpiryDaysForEachNotes,
			MaxProcessingMinutes: cleanMeta.RemoteNotesCleaningMaxProcessingDurationInMinutes,
		}
	}
	cleanProcessor := processors.NewCleanRemoteNotesProcessor(noteRepo, cleanCfg)
	s.queueServer.Handle(queue.TaskTypeCleanRemoteNotes, cleanProcessor.Handle)

	// Expired-mute prune (#1563 / #1603): scheduler の cron (*/5) が enqueue する。
	// user mute と channel mute の両方の期限切れ行を prune する。
	checkExpiredMutingsProcessor := processors.NewCheckExpiredMutingsProcessor(mutingRepo, channelMutingRepo)
	s.queueServer.Handle(queue.TaskTypeCheckExpiredMutings, checkExpiredMutingsProcessor.Handle)

	// Daily generic clean (#1563): scheduler の cron (0 0 * * *) が enqueue する。
	// user_ip 90 日 prune / 期限切れ role_assignment 削除 / reversi outdated game 削除 /
	// 未使用 antenna の deactivate (#1604, deactivateAntennaThreshold ミリ秒)。
	cleanGenericProcessor := processors.NewCleanProcessor(userIPRepo, roleAssignmentRepo, reversiRepo, idGen, antennaRepo, time.Duration(s.config.DeactivateAntennaThreshold)*time.Millisecond)
	s.queueServer.Handle(queue.TaskTypeClean, cleanGenericProcessor.Handle)

	// Reaction flush (issue #57): buffered writer 使用時は 30 秒ごとに flush。
	flushProcessor := processors.NewReactionFlushProcessor(reactionCountWriter)
	s.queueServer.Handle(queue.TaskTypeReactionFlush, flushProcessor.Handle)

	// Account cascade deletion (issue #187): admin/accounts/delete の後続
	// バックグラウンド処理。note / drive_file / following 関連行を掃除する。
	deleteAccountProcessor := processors.NewDeleteAccountProcessor(noteRepo, driveFileRepo, followingRepo)
	s.queueServer.Handle(queue.TaskTypeDeleteAccount, deleteAccountProcessor.Handle)

	// Per-pair Unfollow job (#587): admin/federation/remove-all-following
	// が enqueue するペアごとに Service.Unfollow を呼んで row 削除 + Reject
	// 配送を行う。Misskey TS の relationshipQueue 'unfollow' job 相当。
	unfollowProcessor := processors.NewUnfollowProcessor(followingService)
	s.queueServer.Handle(queue.TaskTypeUnfollow, unfollowProcessor.Handle)

	// Per-pair Follow / Block / Unblock job (#1605): 本家
	// RelationshipProcessorService の processFollow / processBlock /
	// processUnblock 相当。大量関係操作 (将来の AccountMove フォロワー移行
	// 等) の非同期バックプレッシャ surface。import は完了通知の
	// Applied/Skipped 集計を保つため同期適用を維持する (core/transfer)。
	followProcessor := processors.NewFollowProcessor(followingService)
	s.queueServer.Handle(queue.TaskTypeFollow, followProcessor.Handle)
	blockProcessor := processors.NewBlockProcessor(blockingService)
	s.queueServer.Handle(queue.TaskTypeBlock, blockProcessor.Handle)
	unblockProcessor := processors.NewUnblockProcessor(blockingService)
	s.queueServer.Handle(queue.TaskTypeUnblock, unblockProcessor.Handle)
	if bufMeta, err := metaRepo.Fetch(); err == nil && bufMeta.EnableReactionsBuffering {
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				_ = s.queueClient.EnqueueReactionFlush()
			}
		}()
	}

	// Charts (Phase 4 Step Q / 4.7) — 12 chart engines + management
	// service + hook adapter. The hooks must be wired before any
	// service that consumes them is invoked, so we construct the
	// bundle here, before the handler-binding section below.
	chartCharts := buildChartBundle(s.db)
	chartMgmt := chart.NewManagementService([]*chart.Chart{
		chartCharts.Notes,
		chartCharts.Users,
		chartCharts.Drive,
		chartCharts.Federation,
		chartCharts.Instance,
		chartCharts.ApRequest,
		chartCharts.ActiveUsers,
		chartCharts.PerUserNotes,
		chartCharts.PerUserDrive,
		chartCharts.PerUserFollowing,
		chartCharts.PerUserPv,
		chartCharts.PerUserReaction,
	}, 0)
	s.setChartManagement(chartMgmt)
	chartHooks := charthook.New(charthook.Config{
		Notes:            chartCharts.Notes,
		Users:            chartCharts.Users,
		Drive:            chartCharts.Drive,
		Federation:       chartCharts.Federation,
		Instance:         chartCharts.Instance,
		ApRequest:        chartCharts.ApRequest,
		ActiveUsers:      chartCharts.ActiveUsers,
		PerUserNotes:     chartCharts.PerUserNotes,
		PerUserDrive:     chartCharts.PerUserDrive,
		PerUserFollowing: chartCharts.PerUserFollowing,
		PerUserPv:        chartCharts.PerUserPv,
		PerUserReaction:  chartCharts.PerUserReaction,
		IDGen:            idGen,
	})
	// meta フラグでチャート生成を制御する。
	if m, err := metaRepo.Fetch(); err == nil {
		chartHooks.ChartsForRemoteUser = m.EnableChartsForRemoteUser
		chartHooks.ChartsForFederatedInst = m.EnableChartsForFederatedInstances
	}
	// 各サービスへ chart hook を注入する。Set* は nil 安全なので順序は不問。
	noteCreateService.SetChartHook(chartHooks)
	noteDeleteService.SetChartHook(chartHooks)
	followingService.SetChartHook(chartHooks)
	reactionService.SetChartHook(chartHooks)
	driveService.SetChartHook(chartHooks)
	federationResolver.SetChartHook(chartHooks)
	deliverProcessor.SetChartHook(chartHooks)
	inboxProcessor.SetChartHook(chartHooks)
	// federation 経由のリモートノート (Create / Announce) も PerUserNotesChart
	// 等の note 系 chart に +1 を記録するために配線する (#1156)。これが無いと
	// リモートユーザーのプロフィール「アクティビティ」タブの heatmap が空に
	// なり、削除 (-1) だけが note_delete_service 経由で記録されてマイナス側に
	// しか動かなくなる drop-in regression が発生する。
	federationProcessor.SetNoteChartHook(chartHooks)

	// Hashtag service: ノート作成 (local / federation 両経路) で hashtag table の
	// mentionedUsersCount / mentionedUserIds を更新する (#680)。/api/hashtags/list
	// 等の trends ranking はこの集計を見るので、未配線だと空集合のままになる。
	hashtagService := corehashtag.NewService(hashtagRepo, idGen)
	// meta.hiddenTags / meta.sensitiveWords を見て tag の record / featuring
	// を skip するため metaRepo を inject (upstream HashtagService.updateHashtags
	// Ranking と同 semantics、未配線だと sensitive tag が featured に出る
	// drop-in regression が再発するので必ず wire する)。
	hashtagService.SetMetaRepo(metaRepo)
	noteCreateService.SetHashtagHook(hashtagService)
	federationResolver.SetHashtagHook(hashtagService)
	// i/update の profile description 由来 user.tags 変更で hashtag attach 集計
	// (attachedUsersCount) を追従させる (#1362)。
	userService.SetUsertagHook(hashtagService)
	// graceful shutdown 経路で in-flight worker (#719 fire-and-forget) を ctx
	// 期限内に drain する (#727)。typical case では即返り、長時間動く worker は
	// ctx timeout で諦める (idempotent な RecordMention なので次回再カウント)。
	// #764: 旧版は Server.hashtagService field 経由で Shutdown() から呼ばれて
	// いたが、ctx-aware hook 化で field 不要になった。
	s.registerShutdownHook(func(ctx context.Context) {
		if err := hashtagService.Shutdown(ctx); err != nil {
			slog.Warn("hashtag service shutdown timed out", "err", err)
		}
	})

	// Chart cron processor: tickCharts (毎時) / resyncCharts (毎日) /
	// cleanCharts (毎日) を queue.Scheduler 経由で受け取る。Scheduler
	// 自体の cron 登録は server.Start() で行う。
	chartProcessor := processors.NewChartProcessor([]*chart.Chart{
		chartCharts.Notes,
		chartCharts.Users,
		chartCharts.Drive,
		chartCharts.Federation,
		chartCharts.Instance,
		chartCharts.ApRequest,
		chartCharts.ActiveUsers,
		chartCharts.PerUserNotes,
		chartCharts.PerUserDrive,
		chartCharts.PerUserFollowing,
		chartCharts.PerUserPv,
		chartCharts.PerUserReaction,
	})
	s.queueServer.Handle(queue.TaskTypeChartTick, chartProcessor.HandleTick)
	s.queueServer.Handle(queue.TaskTypeChartResync, chartProcessor.HandleResync)
	s.queueServer.Handle(queue.TaskTypeChartClean, chartProcessor.HandleClean)

	// Periodic remote-instance metadata refresh (#393)。
	// `metadataFetcher` は instance service 初期化時に作成済の
	// coreinstance.FetchMetadataService を流用する。
	instanceRefreshProc := processors.NewInstanceRefreshProcessor(instanceRepo, metadataFetcher, processors.InstanceRefreshConfig{})
	s.queueServer.Handle(queue.TaskTypeInstanceRefresh, instanceRefreshProc.Handle)

	// Daily retention aggregation (#421)。
	// 本家 AggregateRetentionProcessorService と同等で、retention_aggregation
	// テーブルに 1 日 1 行 cohort を追加し、過去 31 日分の data フィールドを
	// 更新する。/api/retention と admin overview の「定着率」heatmap が読む。
	retentionSvc := coreretention.NewService(userRepo, retentionRepo, idGen)
	retentionProc := processors.NewRetentionAggregateProcessor(retentionSvc)
	s.queueServer.Handle(queue.TaskTypeRetentionAggregate, retentionProc.Handle)

	// 起動時にも 1 回 aggregation を発火する。cron (0 0 * * * UTC) を待つと
	// 新規デプロイ後最大 24h は heatmap が空のままになるので、その日の
	// cohort 行を即座に作って描画を始められるようにする (#421)。同じ
	// dateKey で 2 回目を Insert しても repository.ErrDuplicateKey で
	// silently 吸収されるので idempotent。DB 書き込みを startup hot path に
	// 乗せないよう goroutine で実行する。
	go func() {
		if err := retentionSvc.Aggregate(context.Background()); err != nil {
			slog.Warn("retention aggregate at startup failed", "err", err)
		}
	}()

	// Health check
	s.echo.GET("/healthz", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Prometheus metrics: enableMetrics=true のときだけ公開する。
	// 認証は付かない (Prometheus 慣例) ので、operator は nginx / LB ACL で
	// access 制限する想定。詳細は docs/design/auto-scale-job-workers.md §6.1。
	if s.config.EnableMetrics {
		if err := wireMetricsEndpoint(s.echo, s.queueMetrics); err != nil {
			slog.Error("server: failed to register queue metrics", "err", err)
		}
	}

	// pprof: enablePprof=true のときだけ公開する。
	// ランタイムプロファイリング用。本番では絶対に有効化してはならない。
	if s.config.EnablePprof {
		pprofGroup := s.echo.Group("/debug/pprof")
		pprofGroup.GET("", func(c echo.Context) error {
			return c.Redirect(http.StatusMovedPermanently, "/debug/pprof/")
		})
		pprofGroup.GET("/", echo.WrapHandler(http.HandlerFunc(pprof.Index)))
		pprofGroup.GET("/cmdline", echo.WrapHandler(http.HandlerFunc(pprof.Cmdline)))
		pprofGroup.GET("/profile", echo.WrapHandler(http.HandlerFunc(pprof.Profile)))
		pprofGroup.GET("/symbol", echo.WrapHandler(http.HandlerFunc(pprof.Symbol)))
		pprofGroup.POST("/symbol", echo.WrapHandler(http.HandlerFunc(pprof.Symbol)))
		pprofGroup.GET("/trace", echo.WrapHandler(http.HandlerFunc(pprof.Trace)))
		pprofGroup.GET("/:name", func(c echo.Context) error {
			pprof.Handler(c.Param("name")).ServeHTTP(c.Response(), c.Request())
			return nil
		})
	}

	api := s.echo.Group("/api")

	// 本家 ApiCallService #sendApiError 互換の WWW-Authenticate 付与 (#1608)。
	// error envelope の kind を見て header を決めるため、応答を最初に
	// 仲介できる位置 (= group 最初の Use) に置く。後続 middleware / handler
	// が書く error 応答 (rate limit 429 / FST_ERR_CTP_* を含む) も全て
	// この writer を通る。
	api.Use(middleware.WWWAuthenticate())

	// Fastify互換のJSON body事前パース (#1609)。本家はbody parseが
	// endpoint handler (rate limit / 認証を含む) より先に走るため、
	// rate limiterより前に登録して malformed JSON を FST_ERR_CTP_* envelope
	// で先に弾く。
	api.Use(middleware.JSONBodyParse())

	// Rate limiter: Redisバックエンドのsliding window、Misskey TS互換。
	// auth.Authenticate()がグローバルミドルウェアとして先に実行されるため、
	// GetUser(c)で認証済みユーザーを取得できる。
	//
	// disableEndpointRateLimits=true のとき per-endpoint table を nil に
	// 落として全 endpoint で 429 を出さない (#560 / Misskey TS の
	// `NODE_ENV=development` 相当)。production では絶対に有効にしない。
	// 起動時の警告は config.resolve() 側で TestMode / EnablePprof と
	// 揃えて出している。
	rateLimitDefs := middleware.DefaultEndpointLimits
	if s.config.DisableEndpointRateLimits {
		rateLimitDefs = nil
	}
	rateLimiter := middleware.NewRedisRateLimiter(
		s.redis.Default,
		s.config.EnableIPRateLimit,
		rateLimitDefs,
	)
	// role policies の rateLimitFactor を反映できるよう roleService を inject
	// (#606 item 4)。trusted user に factor=2.0 等を割り当てて実効 Max を緩和
	// する運用に対応。
	rateLimiter.SetPolicyProvider(roleService)
	api.Use(rateLimiter.Middleware())

	// Meta endpoint (public)
	metaHandler := meta.NewHandler(s.config, metaRepo)
	metaHandler.SetAdRepo(repository.NewAdRepository(s.db))
	proxyAccountResolver := newProxyAccountResolver(repository.NewSystemAccountRepository(s.db), userRepo)
	metaHandler.SetProxyAccountResolver(proxyAccountResolver)
	api.POST("/meta", metaHandler.Meta)
	api.POST("/ping", metaHandler.Ping)

	// Frontend SPA shell — AP resource handlers fall back to this when the
	// request prefers HTML over application/activity+json, and the final
	// catch-all route at the bottom of RegisterRoutes uses the same handler.
	frontend := frontendHTML(s.config, metaRepo, proxyAccountResolver)

	// URL preview endpoint
	if previewMeta, err := metaRepo.Fetch(); err == nil {
		previewCfg := coreurlpreview.Config{
			Enabled:              previewMeta.URLPreviewEnabled,
			AllowRedirect:        previewMeta.URLPreviewAllowRedirect,
			TimeoutMs:            previewMeta.URLPreviewTimeout,
			MaxContentLength:     previewMeta.URLPreviewMaximumContentLength,
			RequireContentLength: previewMeta.URLPreviewRequireContentLength,
		}
		if previewMeta.URLPreviewUserAgent != nil {
			previewCfg.UserAgent = *previewMeta.URLPreviewUserAgent
		}
		if previewMeta.URLPreviewSummaryProxyURL != nil {
			previewCfg.SummaryProxyURL = *previewMeta.URLPreviewSummaryProxyURL
		}
		urlPreviewFetcher := coreurlpreview.NewFetcher(previewCfg, s.redis.Default, s.config.Redis.KeyPrefix(), s.config.AllowedPrivateNetworks, s.outboundOpts()...)
		urlHandler := apiurl.NewHandler(urlPreviewFetcher)
		s.echo.GET("/url", urlHandler.Preview)
	}

	// Test-only endpoints — TestMode=true のときだけ公開する。
	// Cypress の resetState コマンドが依存する /api/reset-db はここで登録する。
	// 本番で絶対に有効化してはならない (config.go 側で起動時 warning を出す)。
	if s.config.TestMode {
		testHandler := apitest.NewHandler(s.db, s.redis.Default, metaRepo, true)
		api.POST("/reset-db", testHandler.ResetDB)
		slog.Warn("test mode: /api/reset-db endpoint is registered", "url", s.config.URL)
	}

	// Stats endpoint (public) — チャートの集計済み値から取得
	notesChart := chartCharts.Notes
	usersChart := chartCharts.Users
	api.POST("/stats", func(c echo.Context) error {
		ctx := c.Request().Context()
		var notesCount, originalNotesCount, usersCount, originalUsersCount int64

		if res, err := notesChart.GetChart(ctx, chart.SpanHour, 1, nil, ""); err == nil {
			if v, ok := res["local.total"]; ok && len(v) > 0 {
				originalNotesCount = v[0]
			}
			if v, ok := res["remote.total"]; ok && len(v) > 0 {
				notesCount = originalNotesCount + v[0]
			}
		}
		if res, err := usersChart.GetChart(ctx, chart.SpanHour, 1, nil, ""); err == nil {
			if v, ok := res["local.total"]; ok && len(v) > 0 {
				originalUsersCount = v[0]
			}
			if v, ok := res["remote.total"]; ok && len(v) > 0 {
				usersCount = originalUsersCount + v[0]
			}
		}

		var instancesCount int64
		s.db.Model(&model.Instance{}).Count(&instancesCount)

		return c.JSON(http.StatusOK, map[string]any{
			"notesCount":         notesCount,
			"originalNotesCount": originalNotesCount,
			"usersCount":         usersCount,
			"originalUsersCount": originalUsersCount,
			"instances":          instancesCount,
			"driveUsageLocal":    0,
			"driveUsageRemote":   0,
			"reactionsCount":     0,
		})
	})

	// Users endpoint (public) — ユーザー一覧
	api.POST("/users", func(c echo.Context) error {
		var req struct {
			Limit  int    `json:"limit"`
			Offset int    `json:"offset"`
			Sort   string `json:"sort"`
			State  string `json:"state"`
			Origin string `json:"origin"`
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusOK, []any{})
		}
		if req.Limit <= 0 {
			req.Limit = 10
		}
		if req.Origin == "" {
			req.Origin = "local"
		}
		users, err := userRepo.ListUsers(model.UserListFilter{
			State: req.State, Origin: req.Origin, Sort: req.Sort,
			Limit: req.Limit, Offset: req.Offset,
		})
		if err != nil {
			return c.JSON(http.StatusOK, []any{})
		}
		result := make([]entity.UserDetailed, 0, len(users))
		for _, u := range users {
			profile, _ := userRepo.FindProfileByUserID(u.ID)
			// idGen を渡して createdAt を有効にする。未配線だと createdAt="" で
			// misskey_dart の DateTimeConverter が FormatException で落ちる (#1251)。
			result = append(result, entity.PackUserDetailed(u, profile, idGen))
		}
		return c.JSON(http.StatusOK, result)
	})

	// Pinned users (public)
	//
	// upstream pinned-users.ts は res が UserDetailed 配列で、各 pinnedUsers の
	// acct を Acct.parse して host?? IsNull() で local/remote 両方を検索する
	// (#1551)。mk-go も acct の host 部分を解決し UserDetailed で返す。
	api.POST("/pinned-users", func(c echo.Context) error {
		m, err := metaRepo.Fetch()
		if err != nil || len(m.PinnedUsers) == 0 {
			return c.JSON(http.StatusOK, []any{})
		}
		result := make([]entity.UserDetailed, 0, len(m.PinnedUsers))
		for _, acct := range m.PinnedUsers {
			username, host := parseAcct(acct, localHost)
			if username == "" {
				continue
			}
			u, err := userRepo.FindByUsernameLower(strings.ToLower(username), host)
			if err != nil {
				continue
			}
			profile, _ := userRepo.FindProfileByUserID(u.ID)
			result = append(result, entity.PackUserDetailed(u, profile, idGen))
		}
		return c.JSON(http.StatusOK, result)
	})

	// CAPTCHA service — meta から有効な provider を選択して構築する。
	// meta 取得失敗時は captcha 無効として動作する (ログイン不能を避けるため)。
	// siteverify は SSRF-safe transport + forward proxy 経由にする (#638)。
	var captchaSvc *corecaptcha.Service
	if serverMeta, err := metaRepo.Fetch(); err == nil {
		captchaSvc = corecaptcha.NewServiceWithClient(serverMeta, s.outboundClient(10*time.Second))
	}

	// Signup (public)
	userPendingRepo := repository.NewUserPendingRepository(s.db)
	signupService.SetUserPendingRepo(userPendingRepo)
	// PromotePending を transaction 化して partial failure rollback と
	// invitation ticket の SELECT FOR UPDATE ロックを有効化する (#600 item 2 + #604)。
	signupService.SetDB(s.db)
	signupTicketRepo := repository.NewRegistrationTicketRepository(s.db)
	signupService.SetTicketRepo(signupTicketRepo)
	signupHandler := apisignup.NewHandler(signupService, metaRepo, idGen)
	if captchaSvc != nil {
		signupHandler.SetCaptcha(captchaSvc)
	}
	// verifymail / truemail SaaS API への outbound を SSRF-safe transport +
	// forward proxy 経由にする (#638)。
	signupHandler.SetEmailValidationClient(s.outboundClient(10 * time.Second))
	// signupTicketRepo は repository.RegistrationTicketRepository で、
	// apisignup.TicketStore (FindByCode + MarkUsed) を superset として満たす。
	// 旧 gormTicketStore wrapper を直接 repo に置き換え (#610 item 1)。
	signupHandler.SetTicketStore(signupTicketRepo)
	signupHandler.SetTestMode(s.config.TestMode)
	// emailRequiredForSignup フローの確認メール送信。常に sender を配線し、
	// closure 内で毎回 meta を読み直すことで admin UI の SMTP 設定変更が
	// 再起動なしに反映される (#1112)。smtpSecure も meta 経由で反映 (#1111)。
	signupHandler.SetEmailSender(s.config.URL, miscsmtp.SenderFromMeta(metaRepo, s.config.ProxySmtp))
	api.POST("/signup", signupHandler.Signup)
	api.POST("/signup-pending", signupHandler.SignupPending)

	// Username availability check (フロントエンドの signup フォームが呼ぶ)
	//
	// upstream username/available.ts は available = (既存 user 無し) && (used_usernames
	// 無し) && (preservedUsernames 非該当) の3条件で判定し、paramDef の
	// localUsernameSchema で format 検証する (#1551)。format 不正は paramDef レベルで
	// 弾かれるため INVALID_PARAM を返す。
	usedUsernameRepo := repository.NewUsedUsernameRepository(s.db)
	api.POST("/username/available", func(c echo.Context) error {
		var req struct {
			Username string `json:"username"`
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, apierr.InvalidParam())
		}
		// format 検証 (upstream localUsernameSchema、paramDef レベル拒否相当)。
		if !coresignup.ValidUsernameFormat(req.Username) {
			return c.JSON(http.StatusBadRequest, apierr.InvalidParam())
		}
		lower := strings.ToLower(req.Username)
		// (1) 既存 local user
		_, userErr := userRepo.FindByUsernameLower(lower, nil)
		existsUser := userErr == nil
		// (2) used_usernames (過去に使われ解放された username)
		usedExists, _ := usedUsernameRepo.Exists(lower)
		// (3) preservedUsernames (予約 username)
		preserved := false
		if m, err := metaRepo.Fetch(); err == nil && m != nil {
			preserved = coresignup.IsReservedUsername(lower, m.PreservedUsernames)
		}
		available := !existsUser && !usedExists && !preserved
		return c.JSON(http.StatusOK, map[string]any{"available": available})
	})

	// Signin (Phase 6)
	signinHandler := apisignin.NewHandler(userRepo)
	if captchaSvc != nil {
		signinHandler.SetCaptcha(captchaSvc)
	}
	// IP logging: meta.enableIpLogging が true のときだけ記録する。
	if serverMeta, err := metaRepo.Fetch(); err == nil && serverMeta.EnableIPLogging {
		signinHandler.SetIPLogger(userIPRepo, true)
	}
	// signin履歴レコード注入
	signinRepo := repository.NewSigninRepository(s.db)
	signinHandler.SetSigninRepo(signinRepo, idGen)
	signinHandler.SetLoginNotifier(notificationHook)
	api.POST("/signin", signinHandler.Signin)
	api.POST("/signin-flow", signinHandler.SigninFlow)
	api.POST("/signin-with-passkey", signinHandler.SigninWithPasskey)

	// パスワードリセット (認証不要)
	resetReqRepo := repository.NewPasswordResetRequestRepository(s.db)
	resetHandler := apiresetpassword.NewHandler(userRepo, resetReqRepo, idGen)
	resetHandler.SetServerURL(s.config.URL)
	// password reset の確認メール送信。配線パターンは signup と同じく
	// SenderFromMeta で per-call 再 Fetch、runtime 設定変更追従 (#1112)。
	resetHandler.SetEmailSender(miscsmtp.SenderFromMeta(metaRepo, s.config.ProxySmtp))
	api.POST("/request-reset-password", resetHandler.RequestReset)
	api.POST("/reset-password", resetHandler.Reset)

	// WebAuthn / 2FA (issue #42)
	// userSecurityKeyRepo + WebAuthnService を構築して signin と /api/i に注入する。
	// Redis セッションは redis.default を流用する (用途別分離は不要)。
	userSecurityKeyRepo := repository.NewUserSecurityKeyRepository(s.db)
	webauthnSvc, webauthnErr := coretwofactor.NewWebAuthnService(s.config.URL, "Misskey", s.redis.Default)
	if webauthnErr != nil {
		slog.Warn("webauthn service unavailable", "err", webauthnErr)
	} else {
		signinHandler.SetWebAuthn(webauthnSvc, userSecurityKeyRepo)
	}

	// TOTP replay guard — accept した TOTP コードを Redis に SETNX で
	// 記録し、acceptance window 内 (~90s) の再利用を refuse する
	// (RFC 6238 §5.2)。upstream Misskey TS は持たない mk-go 独自 hardening。
	// signin と /api/i の 2 経路で共通の guard を共有する。
	totpReplayGuard := coretwofactor.NewRedisReplayGuard(s.redis.Default)
	signinHandler.SetTOTPReplayGuard(totpReplayGuard)

	// Emojis endpoints (public)
	emojisHandler := apiemojis.NewHandler(emojiRepo)
	api.POST("/emojis", emojisHandler.Emojis)
	api.GET("/emojis", emojisHandler.Emojis)
	api.POST("/emoji", emojisHandler.Emoji)
	api.GET("/emoji", emojisHandler.Emoji)

	// 7+ handlers across api/* pack notes via entity.PackNotes. Files /
	// MyReaction / Channel の post-pack 解決は notes 以外でも必要 (#426)。
	// 共通 resolver を 1 つだけ作って全 handler に注入する。
	noteFieldResolver := entity.NewNoteFieldResolver(driveFileRepo, driveFolderRepo, userRepo, reactionRepo, channelRepo, idGen)
	// viewer の poll vote から Poll.choices[i].IsVoted を埋める (#690)。
	// frontend の MkPoll.vue が `showResult = isVoted || closed` を初期化に
	// 使うため、これがないと「結果を見る」toggle が reload で揮発する。
	noteFieldResolver.SetPollVoteLookup(pollVoteRepo)

	// Notes endpoints
	notesHandler := notes.NewHandler(noteRepo, noteCreateService, noteDeleteService, noteQueryService, timelineService, reactionService, pollService, searchService, idGen)
	// first-page timeline 応答を per-viewer 短期キャッシュ (hit 時に DB + pack +
	// encode を skip)。opt-in (enableTimelineCache / MK_ENABLETIMELINECACHE)。
	// staleness trade-off があるため default off。
	if s.config.EnableTimelineCache {
		ttl := time.Duration(s.config.TimelineCacheTTLSeconds) * time.Second
		if ttl <= 0 {
			ttl = 3 * time.Second
		}
		notesHandler.EnableTimelineJSONCache(ttl)
		slog.Info("timeline JSON cache enabled", "ttlSeconds", int(ttl.Seconds()))
	}
	notesHandler.SetDriveFileRepo(driveFileRepo)
	notesHandler.SetPollRepo(pollRepo)                     // #1538: polls/recommendation
	notesHandler.SetThreadMutingRepo(noteThreadMutingRepo) // #1538: thread-muting write
	notesHandler.SetNoteReactionRepo(reactionRepo)
	notesHandler.SetChannelRepo(channelRepo)
	notesHandler.SetChannelMutingRepo(channelMutingRepo)
	// home timeline で follow 中 channel の note を含める (#1686)。
	notesHandler.SetChannelFollowingRepo(channelFollowingRepo)
	// notes/featured の engagement ランキング読み取り (#1687)。
	notesHandler.SetFeaturedRanking(featuredService)
	// timeline endpoint で muted user の note を除外する filter (#874)。
	// 未配線だと user mute は read 時に効かず、cache のみ DB 整合の状態
	// になる security/UX regression が残る。production では必ず wire する。
	notesHandler.SetMutingRepo(mutingRepo)
	// timeline endpoint で renote-muted user の pure renote を除外する
	// filter (#903)。未配線だと renote-mute は read 時に効かず、frontend
	// で消えるべき renote が表示され続ける。production では必ず wire する。
	notesHandler.SetRenoteMutingRepo(renoteMutingRepo)
	notesHandler.SetBlockingRepo(blockingRepo) // #1554: children/replies/renotes mute/block filter
	notesHandler.SetInstanceRepo(instanceRepo)
	notesHandler.SetEmojiRepo(emojiRepo)
	notesHandler.SetReactionReader(reactionCountWriter)
	notesHandler.SetDriveFolderRepo(driveFolderRepo)
	notesHandler.SetUserRepo(userRepo)
	notesHandler.SetModeratorChecker(roleService) // #1538: moderator note delete
	notesHandler.SetUserListRepo(userListRepo)
	notesHandler.SetClipRepos(clipRepo, clipNoteRepo, clipFavoriteRepo) // #1554: notes/clips

	notehide.SetFollowingRepo(followingRepo)
	// LocalTimeline / GlobalTimeline / HybridTimeline で ltlAvailable /
	// gtlAvailable role policy を gate するために配線 (#1026)。匿名 viewer に
	// 対しては GetUserPolicies("") が base policies を返すので、admin が
	// meta.policies で全 user に対して TL を無効化できる。
	notesHandler.SetPolicyProvider(roleService)
	if m, err := metaRepo.Fetch(); err == nil {
		notesHandler.SetUGCVisibility(m.UgcVisibilityForVisitor)
		if m.DeeplAuthKey != nil && *m.DeeplAuthKey != "" {
			notesHandler.SetTranslator(coretranslate.NewDeepL(*m.DeeplAuthKey, m.DeeplIsPro, s.outboundClient(10*time.Second)))
		}
	}
	api.POST("/notes/create", notesHandler.Create, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:notes"))
	api.POST("/notes/show", notesHandler.Show)
	api.POST("/notes/delete", notesHandler.Delete, middleware.RequireAuth(), middleware.RequireScope("write:notes"))
	api.POST("/notes/renotes", notesHandler.Renotes)
	api.POST("/notes/replies", notesHandler.Replies)
	api.POST("/notes/children", notesHandler.Children)
	api.POST("/notes/conversation", notesHandler.Conversation)
	api.POST("/notes/search", notesHandler.Search,
		middleware.RequireAuth(),
		middleware.RequireRolePolicy(roleService, corerole.PolicyCanSearchNotes))
	api.POST("/notes/state", notesHandler.State, middleware.RequireAuth(), middleware.RequireScope("read:account"))
	api.POST("/notes/timeline", notesHandler.Timeline, middleware.RequireAuth(), middleware.RequireScope("read:account"))
	api.POST("/notes/local-timeline", notesHandler.LocalTimeline)
	api.POST("/notes/global-timeline", notesHandler.GlobalTimeline)
	api.POST("/notes/hybrid-timeline", notesHandler.HybridTimeline, middleware.RequireAuth(), middleware.RequireScope("read:account"))
	api.POST("/notes/reactions", notesHandler.Reactions)
	api.GET("/notes/reactions", notesHandler.Reactions)
	api.POST("/notes/reactions/create", notesHandler.ReactionsCreate, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:reactions"))
	api.POST("/notes/reactions/delete", notesHandler.ReactionsDelete, middleware.RequireAuth(), middleware.RequireScope("write:reactions"))
	api.POST("/notes/polls/vote", notesHandler.PollsVote, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:votes"))
	// Notes extra endpoints (Phase 6)
	notesHandler.SetFavoriteRepo(noteFavoriteRepo)
	api.POST("/notes/favorites/create", notesHandler.FavoritesCreate, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:favorites"))
	api.POST("/notes/favorites/delete", notesHandler.FavoritesDelete, middleware.RequireAuth(), middleware.RequireScope("write:favorites"))
	api.POST("/notes/featured", notesHandler.Featured)
	api.GET("/notes/featured", notesHandler.Featured)
	api.POST("/notes/unrenote", notesHandler.Unrenote, middleware.RequireAuth(), middleware.RequireScope("write:notes"))
	api.POST("/notes/mentions", notesHandler.Mentions, middleware.RequireAuth(), middleware.RequireScope("read:account"))
	api.POST("/notes/user-list-timeline", notesHandler.UserListTimeline, middleware.RequireAuth(), middleware.RequireScope("read:account"))
	api.POST("/notes/search-by-tag", notesHandler.SearchByTag)
	api.POST("/notes/clips", notesHandler.Clips)
	api.POST("/notes/translate", notesHandler.Translate,
		middleware.RequireAuth(),
		middleware.RequireRolePolicy(roleService, corerole.PolicyCanUseTranslator), middleware.RequireScope("read:account"))
	api.POST("/notes/show-partial-bulk", notesHandler.ShowPartialBulk)
	// notes/drafts + notes/thread-muting + notes/polls/recommendation (実データ)
	noteDraftRepo := repository.NewNoteDraftRepository(s.db)
	notesHandler.SetDraftRepo(noteDraftRepo)
	notesHandler.SetScheduledNoteEnqueuer(s.queueClient)

	// PostScheduledNote processor (#1040): delayed queue 経由で draft を
	// note 化する。dependencies は既存 noteCreateService + userRepo + 上記
	// noteDraftRepo を共有する。
	postScheduledNoteProcessor := processors.NewPostScheduledNoteProcessor(noteDraftRepo, userRepo, noteCreateService)
	// idempotency lock を Redis SETNX で配線 (#1045 Phase 2-A)。asynq の
	// at-least-once delivery で job が二重 fire しても重複 publish を防ぐ。
	postScheduledNoteProcessor.SetLock(processors.NewRedisScheduledNoteLock(
		s.redis.Default, s.config.Redis.KeyPrefix(), 0))
	// publish 結果通知 (#1045 Phase 2-B): scheduledNotePosted (= 成功) /
	// scheduledNotePostFailed (= 失敗) を user の通知 stream に発火する。
	postScheduledNoteProcessor.SetNotifier(notificationService)
	s.queueServer.Handle(queue.TaskTypePostScheduledNote, postScheduledNoteProcessor.Handle)
	api.POST("/notes/drafts/list", notesHandler.DraftsList, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("read:account"))
	api.POST("/notes/drafts/create", notesHandler.DraftsCreate, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:account"))
	api.POST("/notes/drafts/update", notesHandler.DraftsUpdate, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:account"))
	api.POST("/notes/drafts/delete", notesHandler.DraftsDelete, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:account"))
	api.POST("/notes/drafts/count", notesHandler.DraftsCount, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("read:account"))
	api.POST("/notes/thread-muting/create", notesHandler.ThreadMutingCreate, middleware.RequireAuth(), middleware.RequireScope("write:account"))
	api.POST("/notes/thread-muting/delete", notesHandler.ThreadMutingDelete, middleware.RequireAuth(), middleware.RequireScope("write:account"))
	api.POST("/notes/polls/recommendation", notesHandler.PollsRecommendation, middleware.RequireAuth(), middleware.RequireScope("read:account"))

	// Users endpoints
	usersHandler := users.NewHandler(userService, followingService, noteRepo, idGen)
	usersHandler.SetChartHook(chartHooks)
	usersHandler.SetFeaturedRanking(featuredService) // #1687: users/featured-notes ランキング
	usersHandler.SetPiningRepo(piningRepo)
	usersHandler.SetFollowingRepo(followingRepo)
	usersHandler.SetBlockingRepo(blockingRepo)
	usersHandler.SetMutingRepo(mutingRepo)
	usersHandler.SetRolePolicyProvider(roleService) // #1550: create-from-public userListLimit/userEachUserListsLimit
	usersHandler.SetProxyFollow(listProxyFollow)    // #1704: create-from-public remote member proxy follow
	usersHandler.SetRenoteMutingRepo(renoteMutingRepo)
	usersHandler.SetFollowRequestRepo(followRequestRepo)
	usersHandler.SetInstanceRepo(instanceRepo)
	usersHandler.SetEmojiRepo(emojiRepo)
	usersHandler.SetReactionReader(reactionCountWriter)
	usersHandler.SetClipRepo(clipRepo)
	usersHandler.SetClipFavoriteRepo(clipFavoriteRepo) // #1562: users/clips の favoritedCount / isFavorited
	usersHandler.SetFlashRepo(flashRepo)
	usersHandler.SetGalleryRepo(repository.NewGalleryRepository(s.db))
	usersHandler.SetPageRepo(pageRepo)
	// users/pages の page も attachedFiles / eyeCatchingImage を解決する (#1662)。
	usersHandler.SetDriveFileRepo(driveFileRepo)
	usersHandler.SetNoteFieldResolver(noteFieldResolver)
	usersHandler.SetUserRepo(userRepo)
	usersHandler.SetNoteReactionRepo(reactionRepo)
	usersHandler.SetRemoteStatsFetcher(&remoteStatsFetcherAdapter{
		fetcher: corefederation.NewRemoteStatsFetcher(s.config.AllowedPrivateNetworks, s.config.UserAgent, s.outboundOpts()...),
	})
	api.POST("/users/show", usersHandler.Show)
	api.POST("/users/search", usersHandler.Search,
		middleware.RequireAuth(),
		middleware.RequireRolePolicy(roleService, corerole.PolicyCanSearchUsers))
	api.POST("/users/notes", usersHandler.Notes)
	api.POST("/users/followers", usersHandler.Followers)
	api.POST("/users/following", usersHandler.Following)
	api.POST("/users/relation", usersHandler.Relation, middleware.RequireAuth(), middleware.RequireScope("read:account"))
	api.POST("/users/report-abuse", usersHandler.ReportAbuse, middleware.RequireAuth(), middleware.RequireScope("write:report-abuse"))
	api.POST("/users/reactions", usersHandler.Reactions)
	api.POST("/users/featured-notes", usersHandler.FeaturedNotes)
	api.POST("/users/search-by-username-and-host", usersHandler.SearchByUsernameAndHost,
		middleware.RequireAuth(),
		middleware.RequireRolePolicy(roleService, corerole.PolicyCanSearchUsers))
	api.POST("/users/update-memo", usersHandler.UpdateMemo, middleware.RequireAuth(), middleware.RequireScope("write:account"))
	usersHandler.SetAbuseRepo(repository.NewAbuseReportRepository(s.db))
	usersHandler.SetMemoRepo(repository.NewUserMemoRepository(s.db))
	usersHandler.SetUserListFavoriteRepo(userListFavoriteRepo)
	usersHandler.SetUserListRepo(userListRepo)
	// users/reactions で moderator/admin が remote / 非 public reactions を
	// 閲覧できるようにする (upstream の iAmModerator bypass)。
	usersHandler.SetModeratorChecker(roleService)
	// Phase 7.3: users/* 完全化 (実データハンドラ)
	api.POST("/users/achievements", usersHandler.Achievements)
	api.POST("/users/clips", usersHandler.Clips)
	api.POST("/users/flashs", usersHandler.Flashs)
	api.POST("/users/gallery/posts", usersHandler.GalleryPosts)
	api.POST("/users/pages", usersHandler.Pages)
	api.POST("/users/get-frequently-replied-users", usersHandler.GetFrequentlyRepliedUsers)
	api.POST("/users/get-following-users-by-birthday", usersHandler.GetFollowingUsersByBirthday, middleware.RequireAuth(), middleware.RequireScope("read:account"))
	api.POST("/users/recommendation", usersHandler.UserRecommendation, middleware.RequireAuth(), middleware.RequireScope("read:account"))
	// get-memberships は requireCredential:false の public endpoint (#1550)。
	// RequireScope は app token のみ gate するため public でも残してよい。
	api.POST("/users/lists/get-memberships", usersHandler.ListsGetMemberships, middleware.RequireScope("read:account"))
	api.POST("/users/lists/create-from-public", usersHandler.ListsCreateFromPublic, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:account"))
	api.POST("/users/lists/favorite", usersHandler.ListsFavorite, middleware.RequireAuth(), middleware.RequireScope("write:account"))
	api.POST("/users/lists/unfavorite", usersHandler.ListsUnfavorite, middleware.RequireAuth(), middleware.RequireScope("write:account"))
	api.POST("/users/lists/update", usersHandler.ListsUpdate, middleware.RequireAuth(), middleware.RequireScope("write:account"))
	api.POST("/users/lists/update-membership", usersHandler.ListsUpdateMembership, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:account"))

	// Account endpoints
	registryRepo := repository.NewRegistryRepository(s.db)
	iHandler := i.NewHandler(userService, idGen)
	iHandler.SetUserRepo(userRepo)
	iHandler.SetRoleProvider(roleService)
	iHandler.SetTOTPReplayGuard(totpReplayGuard)
	iHandler.SetRegistryRepo(registryRepo)
	iHandler.SetMetaRepo(metaRepo)
	iHandler.SetServerURL(s.config.URL)
	// i/regenerate-token で旧 token を auth cache から即時削除する (#884)。
	// 未配線だと cache TTL 経過まで旧 token が auth 通過する security
	// regression が残るので production では必ず wire する。
	iHandler.SetAuthInvalidator(s.auth)
	// i/update-email の verifymail / truemail SaaS 呼び出しも SSRF-safe
	// transport + forward proxy 経由にする (#638)。
	iHandler.SetEmailValidationClient(s.outboundClient(10 * time.Second))
	// SMTP メール送信を i/update-email 用に注入する。meta の SMTP 設定に従い、
	// SenderFromMeta が closure 内で per-call 再 Fetch するため admin UI の
	// 設定変更は即座に反映される (#1112)。smtpSecure 反映は #1111。
	iHandler.SetEmailSender(miscsmtp.SenderFromMeta(metaRepo, s.config.ProxySmtp))
	if webauthnSvc != nil {
		iHandler.SetWebAuthn(webauthnSvc, userSecurityKeyRepo)
	}
	iHandler.SetSigninRepo(signinRepo)
	// i/import-* の fileId 所有権検証 (#1555)。未配線だと fail-closed
	// (NO_SUCH_FILE) になるため production では必ず wire する。
	iHandler.SetDriveFileRepo(driveFileRepo)
	// i/import-antennas の TOO_MANY_ANTENNAS 同期 enforce 用 (#1667)。
	iHandler.SetImportDriveReader(driveReader)
	iHandler.SetAntennaCounter(antennaRepo)
	// P4-6 (#166): i/authorized-apps, i/revoke-token, i/gallery/*, i/page-likes
	iHandler.SetAccessTokenRepo(repository.NewAccessTokenRepository(s.db))
	iHandler.SetGalleryRepo(repository.NewGalleryRepository(s.db))
	iHandler.SetPageLikeRepo(pageLikeRepo)
	// P4-6 followup (#174): i/move は federation resolver + deliverService を使って
	// 宛先 actor 解決 → alsoKnownAs 検証 → MovedTo 書き込み → Move activity 配送。
	iHandler.SetAccountMover(coremove.NewService(userRepo, followingRepo, apURLs, apRenderer, federationResolver, deliverService))
	api.POST("/i", iHandler.Me, middleware.RequireAuth(), middleware.RequireScope("read:account"))
	api.POST("/i/update", iHandler.Update, middleware.RequireAuth(), middleware.RequireScope("write:account"))
	api.POST("/i/pin", iHandler.Pin, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:account"))
	api.POST("/i/unpin", iHandler.Unpin, middleware.RequireAuth(), middleware.RequireScope("write:account"))
	api.POST("/i/registry/get", iHandler.RegistryGet, middleware.RequireAuth(), middleware.RequireScope("read:account"))
	api.POST("/i/registry/set", iHandler.RegistrySet, middleware.RequireAuth(), middleware.RequireScope("write:account"))
	api.POST("/i/registry/get-all", iHandler.RegistryGetAll, middleware.RequireAuth(), middleware.RequireScope("read:account"))
	api.POST("/i/registry/keys-with-type", iHandler.RegistryKeysWithType, middleware.RequireAuth(), middleware.RequireScope("read:account"))
	api.POST("/i/registry/remove", iHandler.RegistryRemove, middleware.RequireAuth(), middleware.RequireScope("write:account"))
	api.POST("/i/change-password", iHandler.ChangePassword, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/i/delete-account", iHandler.DeleteAccount, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/i/favorites", iHandler.Favorites, middleware.RequireAuth(), middleware.RequireScope("read:favorites"))
	api.POST("/i/regenerate-token", iHandler.RegenerateToken, middleware.RequireAuth(), middleware.RequireSecure())
	iHandler.SetFavoriteRepo(noteFavoriteRepo)
	// Phase 7-2 (#244): /api/i の未読系フィールドを実クエリ化。
	iHandler.SetNotificationService(notificationService)
	iHandler.SetAchievementNotifier(notificationService)
	iHandler.SetFollowRequestRepo(followRequestRepo)
	iHandler.SetChatRepo(chatRepo)
	// Phase 7-2 follow-up (#271)
	iHandler.SetAntennaUnreadRepo(antennaNoteUnreadRepo)
	iHandler.SetChannelUnreadRepo(channelNoteUnreadRepo)
	// Phase 7-3 (#245): pinnedNoteIds / pinnedNotes / pinnedPageId / pinnedPage
	iHandler.SetPiningRepo(piningRepo)
	iHandler.SetNoteRepo(noteRepo)
	iHandler.SetPageRepo(pageRepo)
	iHandler.SetInstanceRepo(instanceRepo)
	iHandler.SetEmojiRepo(emojiRepo)
	iHandler.SetReactionReader(reactionCountWriter)
	avatarDecorationRepo := repository.NewAvatarDecorationRepository(s.db)
	iHandler.SetAvatarDecorationRepo(avatarDecorationRepo)
	// PackUserLite が avatarDecorations の各エントリに url を埋め込めるよう
	// 共有 catalog resolver を entity package に登録する (#521)。catalog は
	// admin 管理で低頻度更新のため 30s TTL の in-memory cache で十分。
	entity.SetAvatarDecorationLookup(avatardecoration.NewResolver(avatarDecorationRepo))
	// PackUserLite の canChat を role policy 由来 (= upstream
	// `chatAvailability === "available"`) に揃える (#988)。roleService 自体
	// が in-memory cache を持つ (#761) ので追加 DB 負荷は実質ゼロ。
	entity.SetCanChatLookup(corerole.NewCanChatLookup(roleService))
	// PackUserLite / PackUserDetailed の `badgeRoles` / `roles` field を
	// role assignment から populate (#1103)。旧 mk-go は両 field を空配列
	// hard-code していたため、admin が公開ロールを assign しても profile
	// に表示されなかった。roleService.GetUserRoles も in-memory cache 経由
	// (#761) で hot path コスト無し。
	entity.SetUserRolesLookup(corerole.NewUserRolesLookup(roleService))
	// remote user の badgeRoles を出すかは meta.showRoleBadgesOfRemoteUsers
	// 次第 (upstream)。CachedMetaRepository.Fetch は cache 経由で hot path
	// コスト無し。fetch 失敗時は false (= local-only の保守的 default) (#1653)。
	entity.SetShowRemoteBadgesLookup(func() bool {
		m, err := metaRepo.Fetch()
		return err == nil && m != nil && m.ShowRoleBadgesOfRemoteUsers
	})
	// PackDriveFile / IdenticonURL が remote origin の media URL (note 添付・
	// avatar・banner 等) を pack 時に media proxy 経由へ書き換えられるよう
	// context を登録する (#1529)。これが無いと remote の url/thumbnailUrl/
	// avatarUrl が raw のまま <img src> に乗り、閲覧者の IP が remote server
	// に漏れる。proxyRemoteFiles は meta 由来 (既定 true) で boot 時に snapshot
	// する。
	proxyRemoteFiles := true
	if m, err := metaRepo.Fetch(); err == nil {
		proxyRemoteFiles = m.ProxyRemoteFiles
	}
	entity.SetMediaURLContext(entity.NewMediaURLContext(
		s.config.URL,
		s.config.MediaProxy,
		s.config.MediaProxySecret,
		s.config.ExternalMediaProxyEnabled,
		proxyRemoteFiles,
	))
	// PackUserDetailed の isSilenced を role policy 由来に揃える (旧実装は
	// /api/i 以外で常に false 固定だった)。roleService.IsSilenced が
	// entity.SilencedLookup を構造的に満たすので直接 wire する。GetUserRoles は
	// in-memory cache (#761) だが IsSilenced 内の policy merge 自体は per-call
	// なので、UserDetailed pack は単一 user の detail 取得が主用途であり許容範囲。
	entity.SetSilencedLookup(roleService)
	iHandler.SetNoteFieldResolver(noteFieldResolver)
	// announcementRepoは後続で構築されるため SetupAdditional() 相当の順序依存があるが、
	// 現状 announcementRepo := ... の行がここより後にあるため下で wire する。

	// i/export-* and i/import-* (Phase 9.4)
	iHandler.SetTransferEnqueuer(s.queueClient)
	api.POST("/i/export-notes", iHandler.ExportNotes, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/i/export-following", iHandler.ExportFollowing, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/i/export-blocking", iHandler.ExportBlocking, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/i/export-mute", iHandler.ExportMute, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/i/export-favorites", iHandler.ExportFavorites, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/i/export-user-lists", iHandler.ExportUserLists, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/i/export-antennas", iHandler.ExportAntennas, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/i/export-clips", iHandler.ExportClips, middleware.RequireAuth(), middleware.RequireSecure())
	// i/import-* の canImport* role policy gate は #1020 で追加。policy=false
	// の user (default 全 false) は 403 ROLE_PERMISSION_DENIED を返す。
	api.POST("/i/import-following", iHandler.ImportFollowing,
		middleware.RequireAuth(), middleware.RequireNotMoved(),
		middleware.RequireRolePolicy(roleService, corerole.PolicyCanImportFollowing), middleware.RequireSecure())
	api.POST("/i/import-blocking", iHandler.ImportBlocking,
		middleware.RequireAuth(), middleware.RequireNotMoved(),
		middleware.RequireRolePolicy(roleService, corerole.PolicyCanImportBlocking), middleware.RequireSecure())
	api.POST("/i/import-muting", iHandler.ImportMuting,
		middleware.RequireAuth(), middleware.RequireNotMoved(),
		middleware.RequireRolePolicy(roleService, corerole.PolicyCanImportMuting), middleware.RequireSecure())
	api.POST("/i/import-user-lists", iHandler.ImportUserLists,
		middleware.RequireAuth(), middleware.RequireNotMoved(),
		middleware.RequireRolePolicy(roleService, corerole.PolicyCanImportUserLists), middleware.RequireSecure())
	api.POST("/i/import-antennas", iHandler.ImportAntennas,
		middleware.RequireAuth(), middleware.RequireNotMoved(),
		middleware.RequireRolePolicy(roleService, corerole.PolicyCanImportAntennas), middleware.RequireSecure())

	// i/webhooks/* — Webhook管理 (実データ)
	webhookHandler := apiwebhooks.NewHandler(webhookRepo, idGen)
	webhookHandler.SetDispatcher(webhookService)
	webhookHandler.SetRolePolicyProvider(roleService) // #1029: webhookLimit
	api.POST("/i/webhooks/create", webhookHandler.Create, middleware.RequireAuth(), middleware.RequireScope("write:account"))
	api.POST("/i/webhooks/list", webhookHandler.List, middleware.RequireAuth(), middleware.RequireScope("read:account"))
	api.POST("/i/webhooks/show", webhookHandler.Show, middleware.RequireAuth(), middleware.RequireScope("read:account"))
	api.POST("/i/webhooks/update", webhookHandler.Update, middleware.RequireAuth(), middleware.RequireScope("write:account"))
	api.POST("/i/webhooks/delete", webhookHandler.Delete, middleware.RequireAuth(), middleware.RequireScope("write:account"))
	api.POST("/i/webhooks/test", webhookHandler.Test, middleware.RequireAuth(), middleware.RequireSecure(), middleware.RequireScope("read:account"))

	// Notifications endpoints
	notificationsHandler := notifications.NewHandler(notificationService, idGen)
	notificationsHandler.SetRepos(userRepo, noteRepo)
	notificationsHandler.SetQueryService(noteQueryService)
	notificationsHandler.SetFollowRequestRepo(followRequestRepo)
	notificationsHandler.SetInstanceRepo(instanceRepo)
	notificationsHandler.SetEmojiRepo(emojiRepo)
	notificationsHandler.SetTestNotifier(notificationHook)
	notificationsHandler.SetRoleLookup(roleNotifLookup)
	// notifications/create の 'app' 通知で header/icon を token.name/iconUrl に
	// fallback するため access token repo を配線 (#1557)。
	notificationsHandler.SetAccessTokenRepo(repository.NewAccessTokenRepository(s.db))
	api.POST("/i/notifications", notificationsHandler.Show, middleware.RequireAuth(), middleware.RequireScope("read:notifications"))
	api.POST("/i/notifications-grouped", notificationsHandler.Grouped, middleware.RequireAuth(), middleware.RequireScope("read:notifications"))
	api.POST("/notifications/mark-all-as-read", notificationsHandler.MarkAllAsRead, middleware.RequireAuth(), middleware.RequireScope("write:notifications"))
	api.POST("/notifications/create", notificationsHandler.Create, middleware.RequireAuth(), middleware.RequireScope("write:notifications"))
	api.POST("/notifications/flush", notificationsHandler.Flush, middleware.RequireAuth(), middleware.RequireScope("write:notifications"))
	api.POST("/notifications/test-notification", notificationsHandler.TestNotification, middleware.RequireAuth(), middleware.RequireScope("write:notifications"))

	// i/* ハンドラ
	// claim-achievement / import-* は upstream で prohibitMoved:true (移行済
	// アカウントは YOUR_ACCOUNT_MOVED 403)。RequireNotMoved を配線する (#1555)。
	api.POST("/i/claim-achievement", iHandler.ClaimAchievement, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:account"))
	api.POST("/i/apps", iHandler.Apps, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/i/authorized-apps", iHandler.AuthorizedApps, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/i/signin-history", iHandler.SigninHistory, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/i/revoke-token", iHandler.RevokeToken, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/i/update-email", iHandler.UpdateEmail, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/verify-email", iHandler.VerifyEmail)
	api.POST("/i/move", iHandler.Move, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireSecure())
	api.POST("/i/2fa/register", iHandler.TwoFARegister, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/i/2fa/done", iHandler.TwoFADone, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/i/2fa/unregister", iHandler.TwoFAUnregister, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/i/2fa/register-key", iHandler.TwoFARegisterKey, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/i/2fa/key-done", iHandler.TwoFAKeyDone, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/i/2fa/remove-key", iHandler.TwoFARemoveKey, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/i/2fa/update-key", iHandler.TwoFAUpdateKey, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/i/2fa/password-less", iHandler.TwoFAPasswordLess, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/i/gallery/likes", iHandler.GalleryLikes, middleware.RequireAuth(), middleware.RequireScope("read:gallery-likes"))
	api.POST("/i/gallery/posts", iHandler.GalleryPosts, middleware.RequireAuth(), middleware.RequireScope("read:gallery"))
	api.POST("/i/page-likes", iHandler.PageLikes, middleware.RequireAuth(), middleware.RequireScope("read:page-likes"))
	api.POST("/i/registry/get-detail", iHandler.RegistryGetDetail, middleware.RequireAuth(), middleware.RequireScope("read:account"))
	api.POST("/i/registry/keys", iHandler.RegistryKeys, middleware.RequireAuth(), middleware.RequireScope("read:account"))
	api.POST("/i/registry/scopes-with-domain", iHandler.RegistryScopesWithDomain, middleware.RequireAuth(), middleware.RequireSecure())

	// i/export-* / i/import-* は上方 (line ~1245-1265) の explicit registration
	// が active (iHandler.Export* / Import* via transfer_handler.go) で、proper
	// な typed transfer.ExportXxx 定数と fileId 受け取りロジックを持つ。
	// かつて本箇所に generic for-loop による登録が同 path で重複していたが
	// (#1020 で気づいた pre-existing pattern)、それは fileId を受けず string
	// literal "following" 等を直接 type に渡す壊れた version で、Echo の
	// route override 経由で active 実装を蹴る悪影響があった。#1031 で削除済。

	// Hashtags endpoints (Phase 6)
	hashtagsHandler := apihashtags.NewHandler(s.db)
	api.POST("/hashtags/list", hashtagsHandler.List)
	api.POST("/hashtags/search", hashtagsHandler.Search)
	api.POST("/hashtags/show", hashtagsHandler.Show)
	api.POST("/hashtags/trend", hashtagsHandler.Trend)
	// upstream Misskey の hashtags/trend は allowGet: true なので
	// frontend (misskeyApiGet) は GET で叩く。WidgetTrends がここを毎分
	// 呼ぶため GET でも応答できるよう同じ handler を 2 重 wire する。
	api.GET("/hashtags/trend", hashtagsHandler.Trend)
	api.POST("/hashtags/users", hashtagsHandler.Users)

	// Gallery endpoints (Phase 6)
	galleryHandler := apigallery.NewHandler(s.db, idGen)
	galleryHandler.SetRanking(featuredService) // #1548: like/unlike/featured ランキング
	galleryHandler.SetRoleChecker(roleService) // #1548: posts/delete モデレータ削除
	api.POST("/gallery/featured", galleryHandler.Featured)
	api.POST("/gallery/popular", galleryHandler.Popular)
	api.POST("/gallery/posts", galleryHandler.Posts)
	api.POST("/gallery/posts/create", galleryHandler.PostsCreate, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:gallery"))
	api.POST("/gallery/posts/show", galleryHandler.PostsShow)
	api.POST("/gallery/posts/delete", galleryHandler.PostsDelete, middleware.RequireAuth(), middleware.RequireScope("write:gallery"))
	api.POST("/gallery/posts/update", galleryHandler.PostsUpdate, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:gallery"))
	api.POST("/gallery/posts/like", galleryHandler.PostsLike, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:gallery-likes"))
	api.POST("/gallery/posts/unlike", galleryHandler.PostsUnlike, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:gallery-likes"))

	// Blocking endpoints
	blockingHandler := blocking.NewHandler(blockingService, userRepo, idGen)
	api.POST("/blocking/create", blockingHandler.Create, middleware.RequireAuth(), middleware.RequireScope("write:blocks"))
	api.POST("/blocking/delete", blockingHandler.Delete, middleware.RequireAuth(), middleware.RequireScope("write:blocks"))
	api.POST("/blocking/list", blockingHandler.List, middleware.RequireAuth(), middleware.RequireScope("read:blocks"))

	// Mute endpoints
	muteHandler := mute.NewHandler(mutingService, userRepo, idGen)
	api.POST("/mute/create", muteHandler.Create, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:mutes"))
	api.POST("/mute/delete", muteHandler.Delete, middleware.RequireAuth(), middleware.RequireScope("write:mutes"))
	api.POST("/mute/list", muteHandler.List, middleware.RequireAuth(), middleware.RequireScope("read:mutes"))

	// Renote mute endpoints
	renoteMuteHandler := renotemute.NewHandler(renoteMutingService, userRepo, idGen)
	api.POST("/renote-mute/create", renoteMuteHandler.Create, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:mutes"))
	api.POST("/renote-mute/delete", renoteMuteHandler.Delete, middleware.RequireAuth(), middleware.RequireScope("write:mutes"))
	api.POST("/renote-mute/list", renoteMuteHandler.List, middleware.RequireAuth(), middleware.RequireScope("read:mutes"))

	// Drive endpoints
	driveHandler := drive.NewHandler(driveService, idGen)
	driveHandler.SetRepos(driveFileRepo, driveFolderRepo, noteRepo)
	driveHandler.SetUserRepo(userRepo)
	driveHandler.SetInstanceRepo(instanceRepo)
	driveHandler.SetEmojiRepo(emojiRepo)
	driveHandler.SetReactionReader(reactionCountWriter)
	driveHandler.SetNoteFieldResolver(noteFieldResolver)
	api.POST("/drive", driveHandler.Usage, middleware.RequireAuth(), middleware.RequireScope("read:drive"))
	api.POST("/drive/files", driveHandler.FilesList, middleware.RequireAuth(), middleware.RequireScope("read:drive"))
	api.POST("/drive/files/create", driveHandler.FilesCreate, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:drive"))
	api.POST("/drive/files/show", driveHandler.FilesShow, middleware.RequireAuth(), middleware.RequireScope("read:drive"))
	api.POST("/drive/files/update", driveHandler.FilesUpdate, middleware.RequireAuth(), middleware.RequireScope("write:drive"))
	api.POST("/drive/files/delete", driveHandler.FilesDelete, middleware.RequireAuth(), middleware.RequireScope("write:drive"))
	api.POST("/drive/files/find-by-hash", driveHandler.FilesFindByHash, middleware.RequireAuth(), middleware.RequireScope("read:drive"))
	api.POST("/drive/folders/create", driveHandler.FoldersCreate, middleware.RequireAuth(), middleware.RequireScope("write:drive"))
	api.POST("/drive/folders/show", driveHandler.FoldersShow, middleware.RequireAuth(), middleware.RequireScope("read:drive"))
	api.POST("/drive/folders/update", driveHandler.FoldersUpdate, middleware.RequireAuth(), middleware.RequireScope("write:drive"))
	api.POST("/drive/folders/delete", driveHandler.FoldersDelete, middleware.RequireAuth(), middleware.RequireScope("write:drive"))
	api.POST("/drive/folders", driveHandler.FoldersList, middleware.RequireAuth(), middleware.RequireScope("read:drive"))
	api.POST("/drive/folders/find", driveHandler.FoldersFind, middleware.RequireAuth(), middleware.RequireScope("read:drive"))
	api.POST("/drive/files/find", driveHandler.FilesFind, middleware.RequireAuth(), middleware.RequireScope("read:drive"))
	api.POST("/drive/files/check-existence", driveHandler.FilesCheckExistence, middleware.RequireAuth(), middleware.RequireScope("read:drive"))
	api.POST("/drive/files/attached-notes", driveHandler.FilesAttachedNotes, middleware.RequireAuth(), middleware.RequireScope("read:drive"))
	// drive/files/attached-chat-messages は chat message を pack するため chat
	// handler 側で処理する (route 登録は chatHandler 構築後、#1218)。
	api.POST("/drive/files/upload-from-url", driveHandler.FilesUploadFromURL, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:drive"))
	api.POST("/drive/files/move-bulk", driveHandler.FilesMoveBulk, middleware.RequireAuth(), middleware.RequireScope("write:drive"))
	api.POST("/drive/stream", driveHandler.Stream, middleware.RequireAuth(), middleware.RequireScope("read:drive"))
	// Phase 7.4: drive/* — 全て実データハンドラに移行済み

	// Static file serving for LocalStorage / S3 backend. `storedInternal=true`
	// な行は driveStorage が S3 でも localDriveStorage に倒して提供する (#1414)。
	//
	// driveStorage がローカル (= object storage 非活性) の場合は primary と
	// local が同じローカル FS を指すため storedInternal 判定が無意味。この
	// 経路では lookup を nil 配線し、ホットパスである `/files/:accessKey` の
	// 毎リクエスト DB クエリを省く (filesHandler の nil-lookup 分岐に倒す)。
	var filesLookup filesDriveLookup
	if _, isLocal := driveStorage.(*coredrive.LocalStorage); !isLocal {
		filesLookup = driveFileRepo
	}
	s.echo.GET("/files/:accessKey", filesHandler(filesLookup, driveStorage, localDriveStorage))

	// Media proxy endpoint
	proxyAllowlist := coremediaproxy.NewDBAllowlistChecker(s.db)
	proxyService := coremediaproxy.NewService(
		s.config.URL, s.config.UserAgent, driveStorage,
		proxyAllowlist, s.config.MediaProxySecret,
		s.config.AllowedPrivateNetworks,
		s.outboundOpts()...,
	)
	// Local drive file の thumbnail / webpublic 変種を proxy 側で再 encode
	// せず直接返せるようにする (#637 M1)。
	proxyService.SetDriveLookup(driveFileLookupAdapter{repo: driveFileRepo})
	// 動画 still frame の生成は外部 service に委譲する (#637 M2 redesign)。
	// config.videoThumbnailGenerator が空のときは disabled (proxy は dummy
	// PNG を返す)。`unix:///path/socket` 形式で UDS deployment にも対応。
	// videoThumbnailGeneratorMode で wire を選択 ("post" 既定 / "get" は
	// Misskey TS 互換)。
	if s.config.VideoThumbnailGenerator != "" {
		proxyService.SetVideoThumbnailGeneratorWithMode(
			s.config.VideoThumbnailGenerator,
			s.config.VideoThumbnailGeneratorMode,
		)
	}
	proxyHandler := apiproxy.NewHandler(proxyService, s.config)
	s.echo.GET("/proxy/*", proxyHandler.Handle)

	// ActivityPub resource endpoints
	apHandler := ap.NewHandler(apRenderer, userService, noteQueryService, keypairRepo, idGen)
	apHandler.SetRemote(apFetcher, federationResolver)
	// ap/show の federation-allow gate (#1557)。instanceService が blocked /
	// federation policy を判定する。local host は port 無しの hostname
	// (config 起動時に validate 済で非空保証) を渡す — IsBlocked/IsAllowed は
	// hostname suffix match のため。
	apHandler.SetFederationGate(instanceService, s.config.Hostname)
	// FEP-521a Multikey 対応で actor JSON に assertionMethod[] を expose する
	// ため Ed25519 keypair repo を wire (#1067 / #1069)。
	apHandler.SetKeypairExtraRepo(keypairExtraRepo)
	// AP リソース系エンドポイントは Accept ヘッダで content negotiation する。
	// ブラウザからのリロード (Accept: text/html など) では SPA 用の HTML を
	// 返したいので、フォールバックとして frontendHTML を注入しておく。
	apHandler.SetNonAPFallback(frontend)
	s.echo.GET("/users/:id", apHandler.User)
	s.echo.GET("/notes/:id", apHandler.Note)
	s.echo.GET("/@:acct", apHandler.UserByAcct)

	// Discovery endpoints
	wellknownHandler := wellknown.NewHandler(apURLs, userService, s.config.Host, s.config.URL)
	s.echo.GET("/.well-known/webfinger", wellknownHandler.Webfinger)
	s.echo.GET("/.well-known/host-meta", wellknownHandler.HostMeta)
	s.echo.GET("/.well-known/host-meta.json", wellknownHandler.HostMetaJSON)
	s.echo.GET("/.well-known/nodeinfo", wellknownHandler.NodeInfoDiscovery)
	s.echo.GET("/.well-known/oauth-authorization-server", wellknownHandler.OAuthAuthorizationServer)

	nodeinfoHandler := nodeinfo.NewHandler(s.config)
	nodeinfoHandler.SetMetaRepo(metaRepo)
	nodeinfoHandler.SetUsageRepos(userRepo, noteRepo)
	s.echo.GET("/nodeinfo/2.1", nodeinfoHandler.Version2_1)

	// Inbox endpoints
	inboxHandler := inbox.NewHandler(federationResolver, federationProcessor)
	inboxHandler.SetHostBlockChecker(instanceService)
	inboxHandler.SetInstanceTracker(instanceService)
	inboxHandler.SetChartHook(chartHooks)
	// #534: signature 検証成功後の Process(body) を inbox queue に逃がす。
	// 未配線時は legacy 同期処理にフォールバック (テスト互換)。
	inboxHandler.SetEnqueuer(s.queueClient)
	s.echo.POST("/inbox", inboxHandler.Inbox)
	s.echo.POST("/users/:id/inbox", inboxHandler.Inbox)

	// Federation endpoints
	federationHandler := apifederation.NewHandler(instanceService)
	federationHandler.SetFollowingRepo(followingRepo)
	federationHandler.SetUserRepo(userRepo)
	federationHandler.SetIDGen(idGen)
	federationHandler.SetResolver(federationResolver)
	// moderationNote は公開エンドポイントで moderator にのみ返す (情報漏洩対策)。
	federationHandler.SetModeratorChecker(roleService)
	api.POST("/federation/instances", federationHandler.Instances)
	api.GET("/federation/instances", federationHandler.Instances)
	api.POST("/federation/show-instance", federationHandler.ShowInstance)
	api.POST("/federation/followers", federationHandler.Followers)
	api.POST("/federation/following", federationHandler.Following)
	api.POST("/federation/users", federationHandler.Users)
	// admin overview から `misskeyApiGet` で叩かれるため GET も登録 (#421)。
	api.POST("/federation/stats", federationHandler.Stats)
	api.GET("/federation/stats", federationHandler.Stats)
	api.POST("/federation/update-remote-user", federationHandler.UpdateRemoteUser, middleware.RequireModerator(roleService))

	// Channels endpoints (Phase 4.2)
	channelsHandler := apichannels.NewHandler(channelService, idGen)
	channelsHandler.SetFavoriteRepo(channelFavoriteRepo)
	channelsHandler.SetMutingRepo(channelMutingRepo)
	channelsHandler.SetFollowingRepo(channelFollowingRepo)
	channelsHandler.SetDriveFileRepo(driveFileRepo)
	// channels/create の canCreateChannel gate は #1020 で middleware に
	// 昇格 (handler 内 RolePolicyChecker → middleware.RequireRolePolicy)。
	api.POST("/channels/create", channelsHandler.Create,
		middleware.RequireAuth(), middleware.RequireNotMoved(),
		middleware.RequireRolePolicy(roleService, corerole.PolicyCanCreateChannel), middleware.RequireScope("write:channels"))
	api.POST("/channels/show", channelsHandler.Show)
	api.POST("/channels/update", channelsHandler.Update, middleware.RequireAuth(), middleware.RequireScope("write:channels"))
	api.POST("/channels/follow", channelsHandler.Follow, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:channels"))
	api.POST("/channels/unfollow", channelsHandler.Unfollow, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:channels"))
	api.POST("/channels/followed", channelsHandler.Followed, middleware.RequireAuth(), middleware.RequireScope("read:channels"))
	api.POST("/channels/owned", channelsHandler.Owned, middleware.RequireAuth(), middleware.RequireScope("read:channels"))
	api.POST("/channels/featured", channelsHandler.Featured)
	api.POST("/channels/search", channelsHandler.Search)
	api.POST("/channels/timeline", channelsHandler.Timeline)
	channelsHandler.SetInstanceRepo(instanceRepo)
	channelsHandler.SetEmojiRepo(emojiRepo)
	channelsHandler.SetReactionReader(reactionCountWriter)
	channelsHandler.SetNoteFieldResolver(noteFieldResolver)
	channelsHandler.SetUserRepo(userRepo)

	// Antennas endpoints (Phase 4.3)
	antennasHandler := antennas.NewHandler(antennaService, noteRepo, idGen)
	antennasHandler.SetInstanceRepo(instanceRepo)
	antennasHandler.SetEmojiRepo(emojiRepo)
	antennasHandler.SetReactionReader(reactionCountWriter)
	antennasHandler.SetNoteFieldResolver(noteFieldResolver)
	antennasHandler.SetUserRepo(userRepo)
	antennasHandler.SetQueryService(noteQueryService)                              // #1464: Notes の visibility filter (defense-in-depth)
	antennasHandler.SetMuteBlockRepos(mutingRepo, blockingRepo, channelMutingRepo) // #1544: Notes の mute/block/channel-mute filter
	api.POST("/antennas/create", antennasHandler.Create, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:account"))
	api.POST("/antennas/show", antennasHandler.Show, middleware.RequireAuth(), middleware.RequireScope("read:account"))
	api.POST("/antennas/update", antennasHandler.Update, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:account"))
	api.POST("/antennas/delete", antennasHandler.Delete, middleware.RequireAuth(), middleware.RequireScope("write:account"))
	api.POST("/antennas/list", antennasHandler.List, middleware.RequireAuth(), middleware.RequireScope("read:account"))
	api.POST("/antennas/notes", antennasHandler.Notes, middleware.RequireAuth(), middleware.RequireScope("read:account"))

	// Clips endpoints (Phase 4.4)
	clipsHandler := clips.NewHandler(clipService, idGen)
	clipsHandler.SetFavoriteRepo(clipFavoriteRepo)
	clipsHandler.SetInstanceRepo(instanceRepo)
	clipsHandler.SetEmojiRepo(emojiRepo)
	clipsHandler.SetReactionReader(reactionCountWriter)
	clipsHandler.SetNoteFieldResolver(noteFieldResolver)
	clipsHandler.SetUserRepo(userRepo)
	clipsHandler.SetQueryService(noteQueryService)           // #1456: AddNote の visibility gate
	clipsHandler.SetMuteBlockRepos(mutingRepo, blockingRepo) // #1562: Notes の muted/blocked-user filter
	clipsHandler.SetMetaRepo(metaRepo)                       // #1562: Notes の blocked-host filter
	api.POST("/clips/create", clipsHandler.Create, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:account"))
	api.POST("/clips/show", clipsHandler.Show, middleware.RequireScope("read:account"))
	api.POST("/clips/update", clipsHandler.Update, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:account"))
	api.POST("/clips/delete", clipsHandler.Delete, middleware.RequireAuth(), middleware.RequireScope("write:account"))
	api.POST("/clips/list", clipsHandler.List, middleware.RequireAuth(), middleware.RequireScope("read:account"))
	api.POST("/clips/add-note", clipsHandler.AddNote, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:account"))
	api.POST("/clips/remove-note", clipsHandler.RemoveNote, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:account"))
	api.POST("/clips/notes", clipsHandler.Notes, middleware.RequireScope("read:account"))

	// Pages endpoints (Phase 4.5)
	pagesHandler := pages.NewHandler(pageService, idGen)
	// page content の image block / eyeCatchingImageId から drive file を解決して
	// attachedFiles / eyeCatchingImage を埋める (#1662)。
	pagesHandler.SetDriveFileRepo(driveFileRepo)
	api.POST("/pages/create", pagesHandler.Create, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:pages"))
	api.POST("/pages/show", pagesHandler.Show)
	api.POST("/pages/update", pagesHandler.Update, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:pages"))
	api.POST("/pages/delete", pagesHandler.Delete, middleware.RequireAuth(), middleware.RequireScope("write:pages"))
	api.POST("/pages/featured", pagesHandler.Featured)
	api.POST("/pages/like", pagesHandler.Like, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:page-likes"))
	api.POST("/pages/unlike", pagesHandler.Unlike, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:page-likes"))
	api.POST("/i/pages", pagesHandler.My, middleware.RequireAuth(), middleware.RequireScope("read:pages"))

	// Flash endpoints (Phase 4.5)
	flashHandler := apiflash.NewHandler(flashService, userRepo, idGen)
	api.POST("/flash/create", flashHandler.Create, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:flash"))
	api.POST("/flash/show", flashHandler.Show)
	api.POST("/flash/update", flashHandler.Update, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:flash"))
	api.POST("/flash/delete", flashHandler.Delete, middleware.RequireAuth(), middleware.RequireScope("write:flash"))
	api.POST("/flash/featured", flashHandler.Featured)
	api.POST("/flash/search", flashHandler.Search)
	api.POST("/flash/like", flashHandler.Like, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:flash-likes"))
	api.POST("/flash/unlike", flashHandler.Unlike, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:flash-likes"))
	api.POST("/flash/my", flashHandler.My, middleware.RequireAuth(), middleware.RequireScope("read:flash"))
	api.POST("/flash/my-likes", flashHandler.MyLikes, middleware.RequireAuth(), middleware.RequireScope("read:flash-likes"))
	api.POST("/i/flashs", flashHandler.My, middleware.RequireAuth(), middleware.RequireScope("read:flash"))
	api.POST("/i/flashs/likes", flashHandler.MyLikes, middleware.RequireAuth(), middleware.RequireScope("read:flash-likes"))

	// Streaming (Phase 4.1 Step K)
	// 1. Redis pubsub bus (核となる publish/subscribe チャンネル)
	streamPubSub := event.NewPubSubService(s.redis.Pubsub, "stream:")
	streamBus := stream.NewEventPubSubBus(streamPubSub)

	// pollVoted / reacted / unreacted / deleted を noteStream:<id> に publish
	// する共通 publisher。subNote / sn メッセージで購読しているクライアントへ
	// リアクション・削除イベントが流れる (#690 / #700)。pollService /
	// reactionService / noteDeleteService は streamPubSub 生成より前に作るため、
	// ここで後付け配線する。
	noteEventPub := stream.NewNoteEventPublisher(streamPubSub)
	pollService.SetEventPublisher(noteEventPub)
	reactionService.SetNoteStreamHook(&reactionNoteStreamAdapter{pub: noteEventPub})
	noteDeleteService.SetNoteStreamHook(&noteDeleteStreamAdapter{pub: noteEventPub})

	// 2. Channel registry: Misskey 互換のチャンネル名で各 factory を登録する
	streamRegistry := stream.NewRegistry()
	streamRegistry.Register("homeTimeline", channels.NewHomeTimeline)
	streamRegistry.Register("localTimeline", channels.NewLocalTimeline)
	streamRegistry.Register("globalTimeline", channels.NewGlobalTimeline)
	streamRegistry.Register("hybridTimeline", channels.NewHybridTimeline)
	streamRegistry.Register("notifications", channels.NewNotifications)
	streamRegistry.Register("main", channels.NewMain)
	streamRegistry.Register("drive", channels.NewDrive)
	streamRegistry.Register("hashtag", channels.NewHashtag)
	streamRegistry.Register("antenna", channels.NewAntennaFactory(antennaRepo).New)
	streamRegistry.Register("channel", channels.NewChannelTimeline)
	streamRegistry.Register("userList", channels.NewUserList)
	streamRegistry.Register("roleTimeline", channels.NewRoleTimelineFactory(roleService).New)
	streamRegistry.Register("admin", channels.NewAdminFactory(roleService).New)
	// serverStats / queueStats は publisher を後段で構築するため、ここでは
	// 仮 register せず、publisher 生成後に登録する (1497 行付近)。

	// 3. Connection manager + 各 publisher の生成
	streamManager := stream.NewManager(streamRegistry, streamBus)
	// readNotificationメッセージをnotificationServiceに橋渡しする
	streamManager.SetNotificationReader(&notifReaderAdapter{svc: notificationService})
	// hardMutedWords (#787) 用の rules lookup を wire。connection 確立時に
	// 1 度だけ user_profile を引き、以降の publish では cache された rules を
	// 各 timeline channel が参照する。
	streamManager.SetHardMuteLookup(&hardMuteLookupAdapter{userRepo: userRepo})
	// followeeSnapshot (#1063): home/hybrid/local timeline channel が reply gate
	// に使う「followeeID -> withReplies」map を接続確立時に 1 回だけ fetch する。
	// 失敗 / anonymous は nil 返却で degrade — channel は 3 escape hatch のみで
	// 動く (= 旧来の "全 reply drop" よりは upstream 互換に近い)。
	streamManager.SetFollowingSnapshotLookup(&followingSnapshotAdapter{repo: followingRepo})
	// muteBlockSnapshot (#1711): main / notifications channel が notification /
	// mention 配信時に instance-mute / block / mute を gate するための snapshot を
	// 接続確立時に 1 回 fetch する。失敗 / anonymous は nil 返却で fail-open に
	// degrade — filter 無しで動き続ける。
	streamManager.SetMuteBlockSnapshotLookup(&muteBlockSnapshotAdapter{
		muting:        mutingRepo,
		blocking:      blockingRepo,
		renoteMuting:  renoteMutingRepo,
		channelMuting: channelMutingRepo,
		userRepo:      userRepo,
	})
	// subNote visibility gate (#1460 IDOR fix): handleSubNote が任意の noteID
	// で noteStream を subscribe させると、followers / specified note の
	// reacted / unreacted / pollVoted / deleted event が非対象 viewer に
	// 漏れる (HTTP 側 #1444 i/notifications IDOR の WS 版)。noteQueryService
	// の RequireVisible が NoteVisibilityChecker interface を自動 satisfy する。
	streamManager.SetNoteVisibilityChecker(noteQueryService)
	// hardMutedWords 変更時に reload event を受け取って該当 connection の
	// rules を refresh する subscriber を起動 (#791)。i/update 側 publisher
	// と同じ topic 名を共有 (= stream.WordMuteReloadTopic)。
	streamManager.SubscribeWordMuteReload()
	iHandler.SetHardMutePublisher(&hardMutePublisherAdapter{pubsub: streamPubSub})
	// profile 編集を remote followers に Update(Person) で配信する (#1560)。
	// actor endpoint と同じ shape にするため ed25519 keypair を、follower 以外の
	// relay subscriber にも届けるため relay broadcaster を配線する。
	profileUpdateHook := corefederation.NewProfileUpdateDeliveryHook(deliverService, apRenderer, userRepo, keypairRepo)
	profileUpdateHook.SetKeypairExtraRepo(keypairExtraRepo)
	profileUpdateHook.SetRelayBroadcaster(relaySvc)
	iHandler.SetProfileUpdateHook(profileUpdateHook)
	notePublisher := stream.NewNotePublisher(streamPubSub, idGen)
	notePublisher.SetEmojiLookup(emojiRepo)
	notePublisher.SetInstanceLookup(instanceRepo)
	notePublisher.SetReactionReader(reactionCountWriter)
	notePublisher.SetFieldResolver(noteFieldResolver)
	notificationPublisher := stream.NewNotificationPublisher(streamPubSub)
	notificationPublisher.SetRepos(userRepo, noteRepo, idGen)
	notificationPublisher.SetInstanceLookup(instanceRepo)
	notificationPublisher.SetEmojiLookup(emojiRepo)
	// followingRepo: Pack 内 noteVisibleToNotifiee の followers visibility
	// gate (#1471) に必要。未配線時は CanSeeNote semantics に合わせて
	// followers note を本人以外に embed しない (= fail-closed)。
	notificationPublisher.SetFollowingChecker(followingRepo)
	notificationPublisher.SetRoleLookup(roleNotifLookup)
	drivePublisher := stream.NewDrivePublisher(streamPubSub)
	reversiPublisher := stream.NewReversiGamePublisher(streamPubSub)
	mainStreamPublisher := stream.NewMainStreamPublisher(streamPubSub)
	// #1549: report-abuse が各 moderator の adminStream:<id> へ newAbuseUserReport
	// を配信できるよう admin publisher + moderator lister を usersHandler に配線。
	usersHandler.SetAbuseReportFanout(roleService, stream.NewAdminStreamPublisher(streamPubSub))

	// server / queue stats publishers (#344)。起動時から tick を回して
	// `serverStats` / `queueStats` トピックへ定期 publish する。
	// ShutdownでStop()を呼ぶ (server.go 側でdefer)。
	// publisher が ring buffer を持つので、channel factory に StatsLogProvider
	// として渡して requestLog 応答に historical 値を返せるようにする (#571 item 2)。
	serverStatsPub := stream.NewServerStatsPublisher(streamPubSub, 0)
	serverStatsPub.Start()
	s.registerShutdownHook(func(_ context.Context) { serverStatsPub.Stop() })
	streamRegistry.Register("serverStats", channels.NewServerStatsFactory(serverStatsPub))
	if s.queueInspector != nil {
		queueStatsPub := stream.NewQueueStatsPublisher(&queueStatsInspectorAdapter{inner: s.queueInspector}, streamPubSub, 0)
		queueStatsPub.Start()
		s.registerShutdownHook(func(_ context.Context) { queueStatsPub.Stop() })
		streamRegistry.Register("queueStats", channels.NewQueueStatsFactory(queueStatsPub))
	} else {
		// queueInspector が無い起動 (テスト等) では fallback factory で空配列のみ返す
		streamRegistry.Register("queueStats", channels.NewQueueStats)
	}

	// 4. 既存サービスへ publisher を注入する。これらはいずれも nil 安全な
	//    setter で、未設定なら何もしない (テスト互換)。
	timelineFanoutHook.SetStreamingPublisher(notePublisher)
	// antenna realtime 配信 (#1573): FanoutHook と同じ NotePublisher を共有し、
	// OnNoteCreated で match した note を antennaTimeline:<id> へ publish させる。
	// これが無いと WS antenna channel は live note を受信しない (Stream XAdd
	// のみで pubsub publish が欠落していた)。
	antennaService.SetStreamingPublisher(notePublisher)
	notificationService.SetStreamingPublisher(notificationPublisher)
	notificationService.SetMainStreamPublisher(mainStreamPublisher)
	notificationService.SetPacker(notificationPublisher)
	driveService.SetStreamingPublisher(drivePublisher)
	// folder の folderCreated/folderUpdated/folderDeleted も同じ drive channel
	// publisher で配信する (#1564)。
	driveService.SetFolderStreamingPublisher(drivePublisher)
	driveService.SetMainStreamPublisher(mainStreamPublisher)
	// /drive/files/upload-from-url: SSRF-safe client で URL を download し、
	// drive へ保存後 urlUploadFinished を main stream に流す (#1217)。download は
	// 大きめになりうるので 60s timeout、cap は cfg.MaxFileSize に揃える。
	driveHandler.SetURLUploader(drive.NewURLUploader(
		s.outboundClient(60*time.Second), driveService, mainStreamPublisher,
		idGen, s.config.UserAgent, s.config.MaxFileSize,
	))
	followingService.SetMainStreamPublisher(mainStreamPublisher)
	userService.SetMainStreamPublisher(mainStreamPublisher)
	noteCreateService.SetMainStreamPublisher(mainStreamPublisher)
	signinHandler.SetMainStreamPublisher(mainStreamPublisher)
	iHandler.SetMainStreamPublisher(mainStreamPublisher)
	pagesHandler.SetMainStreamPublisher(mainStreamPublisher)
	pagesHandler.SetUserSource(userService)

	// 5. Reversi WebSocket channel (Phase 9.6) を登録する
	reversiService := corereversi.NewService(reversiRepo, reversiPublisher, s.redis.Default)
	streamRegistry.Register("reversiGame", channels.NewReversiGameFactory(reversiService).New)
	streamRegistry.Register("reversi", channels.NewReversi)

	// 6. Chat WebSocket channels (Phase 9.8): chatRoom と chatUser を登録する
	chatPublisher := stream.NewChatPublisher(streamPubSub)
	chatService := corechat.NewService(chatRepo, idGen)
	chatService.SetStreamingPublisher(chatPublisher)
	chatService.SetMainStreamPublisher(mainStreamPublisher)
	// CherryPick 互換 AP 連合: 1-on-1 DM を Create+Note(_misskey_talk:true) で配送 (#692)。
	chatService.SetAPDelivery(userRepo, apRenderer, apURLs, deliverService)
	// chatScope=followers/following/mutual 判定用に following repo を渡す (#692)。
	chatService.SetFollowingRepo(followingRepo)
	// chat/rooms/show の権限 gate で moderator bypass を効かせる
	// (upstream 2026.5.4 hasPermissionToViewRoomInfo 互換、#1164 Phase C)。
	chatService.SetModeratorChecker(roleService)
	// 1-on-1 DM で recipient が sender を block している場合に弾く
	// (upstream YOU_HAVE_BEEN_BLOCKED 互換)。
	chatService.SetBlockingRepo(blockingRepo)
	streamRegistry.Register("chatRoom", channels.NewChatRoomFactory(chatService).New)
	streamRegistry.Register("chatUser", channels.NewChatUserFactory(chatService).New)
	// Phase 9.7: federation processor / reversi handler に reversi 依存を注入。
	// FederationIDCache は本家 Misskey DB スキーマ互換のため DB カラムを持たず
	// Redis のみで session↔gameID の双方向 mapping を保持する。api handler と
	// federation inbox で同じインスタンスを共有する。
	reversiFedCache := corereversi.NewFederationIDCache(s.redis.Default)
	federationProcessor.SetReversi(reversiService, reversiRepo, idGen, reversiFedCache)
	federationProcessor.SetBlockingService(blockingService)
	federationProcessor.SetAbuseReportRepo(repository.NewAbuseReportRepository(s.db), idGen)
	federationProcessor.SetPinningRepo(piningRepo, idGen)
	federationProcessor.SetRelayMarker(relaySvc)
	federationProcessor.SetRelayActorChecker(relaySvc)
	federationProcessor.SetChatService(chatService)
	// group chat (room) federation の inbound 招待処理 (#1203)。chatService が
	// ChatRoomReceiver も実装している。
	federationProcessor.SetChatRoomReceiver(chatService)
	// inbound actor self-delete 時に remote user の notes/drive/following を
	// cascade purge する job を enqueue する (#1220)。
	federationProcessor.SetAccountDeleteEnqueuer(s.queueClient)
	federationProcessor.SetFanoutHook(timelineFanoutHook)
	federationProcessor.SetNotificationHook(notificationHook)

	// 5. /streaming エンドポイント配線
	streamingHandler := streaming.NewHandler(streamManager)
	s.echo.GET("/streaming", streamingHandler.Stream)

	// Charts API endpoints (engines + hooks already wired earlier).
	// フロントエンドは `misskeyApiGet` (GET) でチャートを取得し、
	// `misskeyApi` (POST) も使う場面があるので両方受ける。POST のみ登録だと
	// GET が `api.Any("/*")` の catchall (= 200 + 空オブジェクト) に落ちて、
	// 受信側で `chart.pubActive[0]` 等が `undefined` 例外を起こす (#421)。
	chartsHandler := apicharts.NewHandler(chartCharts)
	chartMethods := []string{http.MethodGet, http.MethodPost}
	api.Match(chartMethods, "/charts/notes", chartsHandler.Notes)
	api.Match(chartMethods, "/charts/users", chartsHandler.Users)
	api.Match(chartMethods, "/charts/drive", chartsHandler.Drive)
	api.Match(chartMethods, "/charts/federation", chartsHandler.Federation)
	api.Match(chartMethods, "/charts/instance", chartsHandler.Instance)
	api.Match(chartMethods, "/charts/ap-request", chartsHandler.ApRequest)
	api.Match(chartMethods, "/charts/active-users", chartsHandler.ActiveUsers)
	api.Match(chartMethods, "/charts/user/notes", chartsHandler.UserNotes)
	api.Match(chartMethods, "/charts/user/drive", chartsHandler.UserDrive)
	api.Match(chartMethods, "/charts/user/following", chartsHandler.UserFollowing)
	api.Match(chartMethods, "/charts/user/pv", chartsHandler.UserPv)
	api.Match(chartMethods, "/charts/user/reactions", chartsHandler.UserReactions)

	// Following endpoints
	followingHandler := following.NewHandler(followingService, userService)
	followingHandler.SetIDGen(idGen)
	api.POST("/following/create", followingHandler.Create, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:following"))
	api.POST("/following/delete", followingHandler.Delete, middleware.RequireAuth(), middleware.RequireScope("write:following"))
	api.POST("/following/list", followingHandler.List, middleware.RequireAuth(), middleware.RequireScope("read:following"))
	api.POST("/following/requests/list", followingHandler.ListRequests, middleware.RequireAuth(), middleware.RequireScope("read:following"))
	api.POST("/following/requests/accept", followingHandler.AcceptRequest, middleware.RequireAuth(), middleware.RequireScope("write:following"))
	api.POST("/following/requests/reject", followingHandler.RejectRequest, middleware.RequireAuth(), middleware.RequireScope("write:following"))
	api.POST("/following/requests/cancel", followingHandler.CancelRequest, middleware.RequireAuth(), middleware.RequireScope("write:following"))
	// following 残り
	api.POST("/following/invalidate", followingHandler.Invalidate, middleware.RequireAuth(), middleware.RequireScope("write:following"))
	api.POST("/following/update", followingHandler.UpdateFollow, middleware.RequireAuth(), middleware.RequireScope("write:following"))
	api.POST("/following/update-all", followingHandler.UpdateFollowAll, middleware.RequireAuth(), middleware.RequireScope("write:following"))
	api.POST("/following/requests/sent", followingHandler.RequestsSent, middleware.RequireAuth(), middleware.RequireScope("read:following"))
	// channels 残り
	api.POST("/channels/favorite", channelsHandler.Favorite, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:channels"))
	api.POST("/channels/unfavorite", channelsHandler.Unfavorite, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:channels"))
	api.POST("/channels/mute/create", channelsHandler.MuteCreate, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:channels"))
	api.POST("/channels/mute/delete", channelsHandler.MuteDelete, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:channels"))
	api.POST("/channels/my-favorites", channelsHandler.MyFavorites, middleware.RequireAuth(), middleware.RequireScope("read:channels"))
	api.POST("/channels/mute/list", channelsHandler.MuteList, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("read:channels"))
	// clips 残り
	api.POST("/clips/favorite", clipsHandler.Favorite, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:clip-favorite"))
	api.POST("/clips/unfavorite", clipsHandler.Unfavorite, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:clip-favorite"))
	api.POST("/clips/my-favorites", clipsHandler.MyFavorites, middleware.RequireAuth(), middleware.RequireScope("read:clip-favorite"))
	// federation の残ルートは上で登録済 (federationHandler 初期化直後)

	// Public roles (Phase 6)
	rolesHandler := apiroles.NewHandler(roleService, idGen)
	rolesHandler.SetNotesQuery(repository.NewRoleNotesQuery(s.db))
	rolesHandler.SetNoteRepo(noteRepo) // #1630: notes の renote 入れ子 mute/block 検査
	rolesHandler.SetInstanceRepo(instanceRepo)
	rolesHandler.SetEmojiRepo(emojiRepo)
	rolesHandler.SetReactionReader(reactionCountWriter)
	rolesHandler.SetNoteFieldResolver(noteFieldResolver)
	rolesHandler.SetUserRepo(userRepo)
	rolesHandler.SetMuteBlockRepos(mutingRepo, blockingRepo, channelMutingRepo) // #1544: Notes の mute/block/channel-mute filter
	api.POST("/roles/list", rolesHandler.List, middleware.RequireAuth(), middleware.RequireScope("read:account"))
	api.POST("/roles/show", rolesHandler.Show)
	api.POST("/roles/users", rolesHandler.Users)
	api.POST("/roles/notes", rolesHandler.Notes, middleware.RequireAuth(), middleware.RequireScope("read:account"))

	// User lists (Phase 6)
	userListHandler := apiuserlists.NewHandler(userListRepo, idGen)
	userListHandler.SetRolePolicyProvider(roleService)                                         // #1029: userListLimit / userEachUserListsLimit
	userListHandler.SetUserRepo(userRepo)                                                      // #1550: push NO_SUCH_USER check
	userListHandler.SetBlockingRepo(blockingRepo)                                              // #1550: push YOU_HAVE_BEEN_BLOCKED check
	userListHandler.SetFavoriteRepo(userListFavoriteRepo)                                      // #1550: show forPublic likedCount/isLiked
	userListHandler.SetProxyFollow(listProxyFollow)                                            // #1704: push remote member proxy follow
	userListHandler.SetUserListEventPublisher(stream.NewUserListStreamPublisher(streamPubSub)) // #1549: push/pull userAdded/userRemoved
	// list は requireCredential:false の public endpoint (userId 指定で他人の
	// public list を閲覧可、#1550)。未認証 && userId 未指定は handler が INVALID_PARAM。
	api.POST("/users/lists/list", userListHandler.List, middleware.RequireScope("read:account"))
	api.POST("/users/lists/create", userListHandler.Create, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:account"))
	api.POST("/users/lists/show", userListHandler.Show, middleware.RequireAuth(), middleware.RequireScope("read:account"))
	api.POST("/users/lists/push", userListHandler.Push, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:account"))
	api.POST("/users/lists/pull", userListHandler.Pull, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:account"))
	api.POST("/users/lists/delete", userListHandler.Delete, middleware.RequireAuth(), middleware.RequireScope("write:account"))

	// Announcements (Phase 6)
	announcementRepo := repository.NewAnnouncementRepository(s.db)

	// Moderator-activity check (#1563): scheduler の cron (30 * * * *) が enqueue
	// する。全 moderator が 7 日非アクティブだと登録を招待制へ自動切替し、残 2 日で
	// 6h ごとに警告メール / announcement / SystemWebhook を送る。announcementRepo /
	// webhookService がここで揃うため本箇所で配線する。announcement は repo 直接
	// ではなく create + announcementCreated publish を対で行う Creator 経由
	// (#1606、本家 AnnouncementService.create 相当)。
	modActivitySvc := coremoderatoractivity.NewService(
		roleService,
		metaRepo,
		userRepo,
		coreannouncement.NewCreator(announcementRepo, idGen, mainStreamPublisher),
		webhookService,
		idGen,
		miscsmtp.SubjectBodySenderFromMeta(metaRepo, s.config.ProxySmtp),
	)
	s.queueServer.Handle(queue.TaskTypeCheckModeratorsActivity,
		processors.NewCheckModeratorsActivityProcessor(modActivitySvc).Handle)

	// Phase 7-2 (#244): /api/i の hasUnreadAnnouncement / unreadAnnouncements 配線
	iHandler.SetAnnouncementRepo(announcementRepo)
	announcementHandler := apiannouncements.NewHandler(announcementRepo, idGen)
	announcementHandler.SetMainStreamPublisher(mainStreamPublisher)
	api.POST("/announcements", announcementHandler.List)
	api.POST("/i/read-announcement", announcementHandler.ReadAnnouncement, middleware.RequireAuth(), middleware.RequireScope("write:account"))

	// Admin endpoints (Phase 5)
	abuseReportRepo := repository.NewAbuseReportRepository(s.db)
	modLogRepo := repository.NewModerationLogRepository(s.db)
	recipientRepo := repository.NewAbuseReportNotificationRecipientRepository(s.db)
	adminHandler := apiadmin.NewHandler(signupService, roleService, metaRepo, userRepo, idGen)
	// admin/suspend-user / admin/unsuspend-user / admin/accounts/delete が
	// target user の全 token cache entry を即時 invalidate するために、
	// auth middleware を inject する (#965)。未配線時は 30 秒 cache TTL 待ち
	// で stale な user が auth 通過する security regression が残るので
	// production では必ず wire する。i/regenerate-token (#884) と同じく
	// AuthMiddleware が duck-typed で UserTokenInvalidator interface を
	// 満たしている。
	adminHandler.SetUserTokenInvalidator(s.auth)
	adminHandler.SetInstanceRepo(instanceRepo)
	// admin/federation/update-instance の suspend / unsuspend を deliver hot path
	// の suspend 判定 cache へ TTL を待たず即時反映する (#1407 review)。
	adminHandler.SetInstanceSuspendCacheInvalidator(instanceService)
	// admin/show-user の signins field を実データで埋める (#1198)。signinRepo
	// は signin handler 配線時に既に構築済 (line ~1030)。
	adminHandler.SetSigninRepo(signinRepo)
	adminHandler.SetAbuseRepo(abuseReportRepo)
	adminHandler.SetAbuseForwarder(coreabuse.NewForwarder(abuseReportRepo, sysAcctSvc, apRenderer, deliverService))
	adminHandler.SetDeleteAccountEnqueuer(s.queueClient)
	adminHandler.SetPasswordResetRepo(resetReqRepo)
	adminHandler.SetServerURL(s.config.URL)
	adminHandler.SetConfigSetupPassword(s.config.SetupPassword)
	adminHandler.SetSMTPProxyURL(s.config.ProxySmtp)
	// admin/reset-password の確認メール送信。per-call 再 Fetch (#1112) +
	// smtpSecure 反映 (#1111)。admin.EmailSender は (to, subject, body)
	// 形式なので SubjectBodySenderFromMeta で wrap する。
	adminHandler.SetEmailSender(miscsmtp.SubjectBodySenderFromMeta(metaRepo, s.config.ProxySmtp))
	modLogService := coremodlog.New(modLogRepo, idGen)
	adminHandler.SetModLogService(modLogService)
	// announcements (別パッケージの Handler) も AdminCreate/Update/Delete で
	// modlog を書くため同じ service instance を共有する。
	announcementHandler.SetModLogService(modLogService)
	announcementHandler.SetUserRepo(userRepo)
	galleryHandler.SetModLog(modLogService) // #1548: moderator による gallery post 削除の監査ログ
	adminHandler.SetEmojiRepo(emojiRepo)
	adminHandler.SetDriveFileRepo(driveFileRepo)
	// bulk drive cleanup (clean-remote-files / delete-all-files-of-a-user) で
	// object storage の物理オブジェクトも消すため storage backend を渡す。
	adminHandler.SetStorageDeleter(driveStorage)
	adminHandler.SetAdminDB(s.db)
	adminHandler.SetUserIPRepo(userIPRepo)
	adminHandler.SetEmojiImportEnqueuer(s.queueClient)
	// admin/emoji/copy で remote 画像を local drive に保存するための fetcher
	// を wire する (#670)。outboundClient 経由なので SSRF / proxy / outgoing
	// address が他の outbound 経路と同じ設定で適用される。
	adminHandler.SetEmojiImageFetcher(apiadmin.NewEmojiImageFetcher(s.outboundClient(10*time.Second), driveService, s.config.UserAgent))
	adminHandler.SetRelayService(relaySvc)
	adminHandler.SetSystemWebhookRepo(systemWebhookRepo)
	// admin/system-webhook/test の fire-and-forget POST も SSRF-safe transport
	// + forward proxy 経由にする (#638)。
	adminHandler.SetWebhookTestClient(s.outboundClient(10 * time.Second))
	adminHandler.SetRecipientRepo(recipientRepo)
	adminHandler.SetAdRepo(repository.NewAdRepository(s.db))
	adminHandler.SetAvatarDecorationRepo(repository.NewAvatarDecorationRepository(s.db))
	adminHandler.SetInviteRepo(repository.NewRegistrationTicketRepository(s.db))
	adminHandler.SetPromoNoteRepo(repository.NewPromoNoteRepository(s.db))
	adminHandler.SetNoteFinder(noteRepo)
	if s.queueInspector != nil {
		adminHandler.SetQueueInspector(&queueInspectorAdapter{inner: s.queueInspector})
	}
	// per-queue db (Redis INFO: memory / clients / uptime) を admin queue 画面に
	// 出すため job-queue Redis を配線 (#1393)。
	if s.redis != nil && s.redis.JobQueue != nil {
		adminHandler.SetQueueRedis(jobQueueRedisInfo{rdb: s.redis.JobQueue})
	}
	adminHandler.SetInstanceMetadataFetcher(metadataFetcher)
	adminHandler.SetSystemAccountFetcher(sysAcctSvc)
	// admin/federation/remove-all-following が host 単位で全 follower を
	// detach するために followingRepo + Unfollow enqueuer が必要 (#587)。
	// 実 row 削除と Reject(Follow) 配送は worker (UnfollowProcessor) が
	// followingService.Unfollow を呼んで行う。本家 TS の queueService.
	// createUnfollowJob と等価。
	adminHandler.SetFollowingRepo(followingRepo)
	adminHandler.SetUnfollowEnqueuer(s.queueClient)
	api.POST("/admin/accounts/create", adminHandler.AccountsCreate)
	api.POST("/admin/show-user", adminHandler.ShowUser, middleware.RequireModerator(roleService), middleware.RequireScope("read:admin:show-user"))
	api.POST("/admin/show-users", adminHandler.ShowUsers, middleware.RequireModerator(roleService), middleware.RequireScope("read:admin:show-user"))
	api.POST("/admin/suspend-user", adminHandler.SuspendUser, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:suspend-user"))
	api.POST("/admin/unsuspend-user", adminHandler.UnsuspendUser, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:unsuspend-user"))
	api.POST("/admin/meta", adminHandler.AdminMeta, middleware.RequireAdmin(roleService), middleware.RequireScope("read:admin:meta"))
	api.POST("/admin/update-meta", adminHandler.UpdateMeta, middleware.RequireAdmin(roleService), middleware.RequireScope("write:admin:meta"))
	api.POST("/admin/roles/create", adminHandler.RolesCreate, middleware.RequireAdmin(roleService), middleware.RequireScope("write:admin:roles"))
	api.POST("/admin/roles/show", adminHandler.RolesShow, middleware.RequireModerator(roleService), middleware.RequireScope("read:admin:roles"))
	api.POST("/admin/roles/list", adminHandler.RolesList, middleware.RequireModerator(roleService), middleware.RequireScope("read:admin:roles"))
	api.POST("/admin/roles/update", adminHandler.RolesUpdate, middleware.RequireAdmin(roleService), middleware.RequireScope("write:admin:roles"))
	api.POST("/admin/roles/delete", adminHandler.RolesDelete, middleware.RequireAdmin(roleService), middleware.RequireScope("write:admin:roles"))
	api.POST("/admin/roles/assign", adminHandler.RolesAssign, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:roles"))
	api.POST("/admin/roles/unassign", adminHandler.RolesUnassign, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:roles"))
	api.POST("/admin/roles/users", adminHandler.RolesUsers, middleware.RequireModerator(roleService), middleware.RequireScope("read:admin:roles"))
	api.POST("/admin/roles/update-default-policies", adminHandler.RolesUpdateDefaultPolicies, middleware.RequireAdmin(roleService), middleware.RequireScope("write:admin:roles"))
	api.POST("/admin/abuse-user-reports", adminHandler.AbuseReports, middleware.RequireModerator(roleService), middleware.RequireScope("read:admin:abuse-user-reports"))
	api.POST("/admin/resolve-abuse-user-report", adminHandler.ResolveAbuseReport, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:resolve-abuse-user-report"))
	api.POST("/admin/show-moderation-logs", adminHandler.ShowModerationLogs, middleware.RequireAdmin(roleService), middleware.RequireScope("read:admin:show-moderation-log"))
	// admin/emoji/* と admin/avatar-decorations/* は upstream Misskey TS の
	// ApiCallService.ts が requiredRolePolicy (canManageCustomEmojis /
	// canManageAvatarDecorations) のみで gate する (requireModerator/Admin
	// flag は設定されていない) ため、mk-go も RequireRolePolicy に揃える
	// (#1025)。admin role 持ちは HasRolePolicy 内で常に true → 自動 bypass、
	// それ以外は role で明示的に policy=true 必要。
	api.POST("/admin/emoji/add", adminHandler.EmojiAdd, middleware.RequireRolePolicy(roleService, corerole.PolicyCanManageCustomEmojis), middleware.RequireScope("write:admin:emoji"))
	api.POST("/admin/emoji/update", adminHandler.EmojiUpdate, middleware.RequireRolePolicy(roleService, corerole.PolicyCanManageCustomEmojis), middleware.RequireScope("write:admin:emoji"))
	api.POST("/admin/emoji/delete", adminHandler.EmojiDelete, middleware.RequireRolePolicy(roleService, corerole.PolicyCanManageCustomEmojis), middleware.RequireScope("write:admin:emoji"))
	api.POST("/admin/emoji/list", adminHandler.EmojiList, middleware.RequireRolePolicy(roleService, corerole.PolicyCanManageCustomEmojis), middleware.RequireScope("read:admin:emoji"))
	api.POST("/admin/announcements/create", announcementHandler.AdminCreate, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:announcements"))
	api.POST("/admin/announcements/update", announcementHandler.AdminUpdate, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:announcements"))
	api.POST("/admin/announcements/delete", announcementHandler.AdminDelete, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:announcements"))
	api.POST("/admin/announcements/list", announcementHandler.AdminList, middleware.RequireModerator(roleService), middleware.RequireScope("read:admin:announcements"))

	// Phase 7.5: admin/* 残りエンドポイント (ハンドラメソッド化)
	api.POST("/admin/delete-account", adminHandler.DeleteAccount, middleware.RequireAdmin(roleService), middleware.RequireScope("write:admin:delete-account"))
	api.POST("/admin/delete-all-files-of-a-user", adminHandler.DeleteAllFilesOfUser, middleware.RequireAdmin(roleService), middleware.RequireScope("write:admin:delete-all-files-of-a-user"))
	api.POST("/admin/reset-password", adminHandler.ResetPassword, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:reset-password"))
	api.POST("/admin/send-email", adminHandler.SendEmail, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:send-email"))
	api.POST("/admin/unset-user-avatar", adminHandler.UnsetUserAvatar, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:unset-user-avatar"))
	api.POST("/admin/unset-user-banner", adminHandler.UnsetUserBanner, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:unset-user-banner"))
	api.POST("/admin/update-user-note", adminHandler.UpdateUserNote, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:user-note"))
	api.POST("/admin/update-proxy-account", adminHandler.UpdateProxyAccount, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:account"))
	api.POST("/admin/forward-abuse-user-report", adminHandler.ForwardAbuseUserReport, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:resolve-abuse-user-report"))
	api.POST("/admin/update-abuse-user-report", adminHandler.UpdateAbuseUserReport, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:resolve-abuse-user-report"))
	api.POST("/admin/accounts/delete", adminHandler.AccountsDelete, middleware.RequireAdmin(roleService), middleware.RequireScope("write:admin:account"))
	api.POST("/admin/accounts/find-by-email", adminHandler.AccountsFindByEmail, middleware.RequireAdmin(roleService), middleware.RequireScope("read:admin:account"))
	api.POST("/admin/get-user-ips", adminHandler.GetUserIPs, middleware.RequireAdmin(roleService), middleware.RequireScope("read:admin:user-ips"))
	api.POST("/admin/get-index-stats", adminHandler.GetIndexStats, middleware.RequireAdmin(roleService), middleware.RequireScope("read:admin:index-stats"))
	api.POST("/admin/get-table-stats", adminHandler.GetTableStats, middleware.RequireAdmin(roleService), middleware.RequireScope("read:admin:table-stats"))
	api.POST("/admin/server-info", adminHandler.ServerInfo, middleware.RequireModerator(roleService), middleware.RequireScope("read:admin:server-info"))
	api.POST("/admin/captcha/current", adminHandler.CaptchaCurrent, middleware.RequireAdmin(roleService), middleware.RequireScope("read:admin:meta"))
	api.POST("/admin/captcha/save", adminHandler.CaptchaSave, middleware.RequireAdmin(roleService), middleware.RequireScope("write:admin:meta"))
	api.POST("/admin/ad/create", adminHandler.AdCreate, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:ad"))
	api.POST("/admin/ad/delete", adminHandler.AdDelete, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:ad"))
	api.POST("/admin/ad/list", adminHandler.AdList, middleware.RequireModerator(roleService), middleware.RequireScope("read:admin:ad"))
	api.POST("/admin/ad/update", adminHandler.AdUpdate, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:ad"))
	api.POST("/admin/avatar-decorations/create", adminHandler.AvatarDecorationsCreate, middleware.RequireRolePolicy(roleService, corerole.PolicyCanManageAvatarDecorations), middleware.RequireScope("write:admin:avatar-decorations"))
	api.POST("/admin/avatar-decorations/delete", adminHandler.AvatarDecorationsDelete, middleware.RequireRolePolicy(roleService, corerole.PolicyCanManageAvatarDecorations), middleware.RequireScope("write:admin:avatar-decorations"))
	api.POST("/admin/avatar-decorations/list", adminHandler.AvatarDecorationsList, middleware.RequireRolePolicy(roleService, corerole.PolicyCanManageAvatarDecorations), middleware.RequireScope("read:admin:avatar-decorations"))
	api.POST("/admin/avatar-decorations/update", adminHandler.AvatarDecorationsUpdate, middleware.RequireRolePolicy(roleService, corerole.PolicyCanManageAvatarDecorations), middleware.RequireScope("write:admin:avatar-decorations"))
	api.POST("/admin/drive/clean-remote-files", adminHandler.DriveCleanRemoteFiles, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:drive"))
	api.POST("/admin/drive/cleanup", adminHandler.DriveCleanup, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:drive"))
	api.POST("/admin/drive/files", adminHandler.DriveFiles, middleware.RequireModerator(roleService), middleware.RequireScope("read:admin:drive"))
	api.POST("/admin/drive/show-file", adminHandler.DriveShowFile, middleware.RequireModerator(roleService), middleware.RequireScope("read:admin:drive"))
	api.POST("/admin/emoji/add-aliases-bulk", adminHandler.EmojiAddAliasesBulk, middleware.RequireRolePolicy(roleService, corerole.PolicyCanManageCustomEmojis), middleware.RequireScope("write:admin:emoji"))
	api.POST("/admin/emoji/copy", adminHandler.EmojiCopy, middleware.RequireRolePolicy(roleService, corerole.PolicyCanManageCustomEmojis), middleware.RequireScope("write:admin:emoji"))
	api.POST("/admin/emoji/delete-bulk", adminHandler.EmojiDeleteBulk, middleware.RequireRolePolicy(roleService, corerole.PolicyCanManageCustomEmojis), middleware.RequireScope("write:admin:emoji"))
	api.POST("/admin/emoji/import-zip", adminHandler.EmojiImportZip, middleware.RequireRolePolicy(roleService, corerole.PolicyCanManageCustomEmojis), middleware.RequireSecure())
	api.POST("/admin/emoji/list-remote", adminHandler.EmojiListRemote, middleware.RequireRolePolicy(roleService, corerole.PolicyCanManageCustomEmojis), middleware.RequireScope("read:admin:emoji"))
	api.POST("/admin/emoji/remove-aliases-bulk", adminHandler.EmojiRemoveAliasesBulk, middleware.RequireRolePolicy(roleService, corerole.PolicyCanManageCustomEmojis), middleware.RequireScope("write:admin:emoji"))
	api.POST("/admin/emoji/set-aliases-bulk", adminHandler.EmojiSetAliasesBulk, middleware.RequireRolePolicy(roleService, corerole.PolicyCanManageCustomEmojis), middleware.RequireScope("write:admin:emoji"))
	api.POST("/admin/emoji/set-category-bulk", adminHandler.EmojiSetCategoryBulk, middleware.RequireRolePolicy(roleService, corerole.PolicyCanManageCustomEmojis), middleware.RequireScope("write:admin:emoji"))
	api.POST("/admin/emoji/set-license-bulk", adminHandler.EmojiSetLicenseBulk, middleware.RequireRolePolicy(roleService, corerole.PolicyCanManageCustomEmojis), middleware.RequireScope("write:admin:emoji"))
	api.POST("/admin/federation/delete-all-files", adminHandler.FederationDeleteAllFiles, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:federation"))
	api.POST("/admin/federation/refresh-remote-instance-metadata", adminHandler.FederationRefreshRemoteInstanceMetadata, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:federation"))
	api.POST("/admin/federation/remove-all-following", adminHandler.FederationRemoveAllFollowing, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:federation"))
	api.POST("/admin/federation/update-instance", adminHandler.FederationUpdateInstance, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:federation"))
	api.POST("/admin/invite/create", adminHandler.InviteCreate, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:invite-codes"))
	api.POST("/admin/invite/list", adminHandler.InviteList, middleware.RequireModerator(roleService), middleware.RequireScope("read:admin:invite-codes"))
	api.POST("/admin/promo/create", adminHandler.PromoCreate, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:promo"))
	api.POST("/admin/relays/add", adminHandler.RelaysAdd, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:relays"))
	api.POST("/admin/relays/list", adminHandler.RelaysList, middleware.RequireModerator(roleService), middleware.RequireScope("read:admin:relays"))
	api.POST("/admin/relays/remove", adminHandler.RelaysRemove, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:relays"))
	api.POST("/admin/system-webhook/create", adminHandler.SystemWebhookCreate, middleware.RequireModerator(roleService), middleware.RequireSecure(), middleware.RequireScope("write:admin:system-webhook"))
	api.POST("/admin/system-webhook/delete", adminHandler.SystemWebhookDelete, middleware.RequireModerator(roleService), middleware.RequireSecure(), middleware.RequireScope("write:admin:system-webhook"))
	api.POST("/admin/system-webhook/list", adminHandler.SystemWebhookList, middleware.RequireModerator(roleService), middleware.RequireSecure(), middleware.RequireScope("write:admin:system-webhook"))
	api.POST("/admin/system-webhook/show", adminHandler.SystemWebhookShow, middleware.RequireModerator(roleService), middleware.RequireSecure(), middleware.RequireScope("write:admin:system-webhook"))
	api.POST("/admin/system-webhook/test", adminHandler.SystemWebhookTest, middleware.RequireModerator(roleService), middleware.RequireSecure(), middleware.RequireScope("read:admin:system-webhook"))
	api.POST("/admin/system-webhook/update", adminHandler.SystemWebhookUpdate, middleware.RequireModerator(roleService), middleware.RequireSecure(), middleware.RequireScope("write:admin:system-webhook"))
	api.POST("/admin/abuse-report/notification-recipient/create", adminHandler.AbuseReportNotificationRecipientCreate, middleware.RequireModerator(roleService), middleware.RequireSecure(), middleware.RequireScope("write:admin:abuse-report:notification-recipient"))
	api.POST("/admin/abuse-report/notification-recipient/delete", adminHandler.AbuseReportNotificationRecipientDelete, middleware.RequireModerator(roleService), middleware.RequireSecure(), middleware.RequireScope("write:admin:abuse-report:notification-recipient"))
	api.POST("/admin/abuse-report/notification-recipient/list", adminHandler.AbuseReportNotificationRecipientList, middleware.RequireModerator(roleService), middleware.RequireSecure(), middleware.RequireScope("read:admin:abuse-report:notification-recipient"))
	api.POST("/admin/abuse-report/notification-recipient/show", adminHandler.AbuseReportNotificationRecipientShow, middleware.RequireModerator(roleService), middleware.RequireSecure(), middleware.RequireScope("read:admin:abuse-report:notification-recipient"))
	api.POST("/admin/abuse-report/notification-recipient/update", adminHandler.AbuseReportNotificationRecipientUpdate, middleware.RequireModerator(roleService), middleware.RequireSecure(), middleware.RequireScope("write:admin:abuse-report:notification-recipient"))
	api.POST("/admin/queue/clear", adminHandler.QueueClear, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:queue"))
	api.POST("/admin/queue/deliver-delayed", adminHandler.QueueDeliverDelayed, middleware.RequireModerator(roleService), middleware.RequireScope("read:admin:queue"))
	api.POST("/admin/queue/inbox-delayed", adminHandler.QueueInboxDelayed, middleware.RequireModerator(roleService), middleware.RequireScope("read:admin:queue"))
	api.POST("/admin/queue/jobs", adminHandler.QueueJobs, middleware.RequireModerator(roleService), middleware.RequireScope("read:admin:queue"))
	api.POST("/admin/queue/promote-jobs", adminHandler.QueuePromoteJobs, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:queue"))
	api.POST("/admin/queue/queue-stats", adminHandler.QueueQueueStats, middleware.RequireModerator(roleService), middleware.RequireScope("read:admin:queue"))
	api.POST("/admin/queue/queues", adminHandler.QueueQueues, middleware.RequireModerator(roleService), middleware.RequireScope("read:admin:queue"))
	api.POST("/admin/queue/remove-job", adminHandler.QueueRemoveJob, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:queue"))
	api.POST("/admin/queue/retry-job", adminHandler.QueueRetryJob, middleware.RequireModerator(roleService), middleware.RequireScope("write:admin:queue"))
	api.POST("/admin/queue/show-job", adminHandler.QueueShowJob, middleware.RequireModerator(roleService), middleware.RequireScope("read:admin:queue"))
	api.POST("/admin/queue/show-job-logs", adminHandler.QueueShowJobLogs, middleware.RequireModerator(roleService), middleware.RequireScope("read:admin:queue"))
	api.POST("/admin/queue/stats", adminHandler.QueueStats, middleware.RequireModerator(roleService), middleware.RequireScope("read:admin:emoji"))

	// --- Phase 7.6b: chat/*, auth/*, ap/*, sw/*, reversi/*, bubble-game/*, misc ---

	// announcements/show — 個別アナウンスメント取得
	api.POST("/announcements/show", announcementHandler.Show)

	// auth/* — MiAuth/OAuth セッション
	authSessionRepo := repository.NewAuthSessionRepository(s.db)
	authHandler := apiauth.NewHandler(authSessionRepo, s.config, idGen)
	api.POST("/auth/session/generate", authHandler.SessionGenerate)
	api.POST("/auth/session/show", authHandler.SessionShow)
	api.POST("/auth/session/userkey", authHandler.SessionUserkey)
	api.POST("/auth/accept", authHandler.Accept, middleware.RequireAuth(), middleware.RequireSecure())

	// miauth/gen-token — アクセストークン直接生成
	api.POST("/miauth/gen-token", authHandler.GenToken, middleware.RequireAuth(), middleware.RequireSecure())
	// miauth/:session/check — 3rd-party client が session 紐付き token を取得する
	// (#1224)。token をまだ持たない client が叩くため RequireAuth は付けない。
	authHandler.SetUserRepo(userRepo)
	authHandler.SetTokenNotifier(notificationHook)
	api.POST("/miauth/:session/check", authHandler.MiAuthCheck)

	// app/* — アプリ管理API
	appHandler := apiapp.NewHandler(authSessionRepo, idGen)
	api.POST("/app/create", appHandler.Create)
	api.POST("/app/show", appHandler.Show)
	api.POST("/my/apps", appHandler.MyApps, middleware.RequireAuth(), middleware.RequireScope("read:account"))

	// ap/* — ActivityPub API lookup (実データ: ローカルオブジェクト解決)
	// /api/ap/notes は upstream Misskey にも存在しない vestigial route だった
	// ため #587 で削除済 (旧実装は常に空配列返却の stub)。
	api.POST("/ap/get", apHandler.APIGet, middleware.RequireAdmin(roleService), middleware.RequireScope("read:federation"))
	api.POST("/ap/show", apHandler.APIShow, middleware.RequireAuth(), middleware.RequireScope("read:account"))

	// sw/* — Service Worker push notifications (実データ)
	swHandler := apisw.NewHandler(swSubRepo, metaRepo, idGen)
	api.POST("/sw/register", swHandler.Register, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/sw/show-registration", swHandler.ShowRegistration, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/sw/update-registration", swHandler.UpdateRegistration, middleware.RequireAuth(), middleware.RequireSecure())
	api.POST("/sw/unregister", swHandler.Unregister)

	// reversi/* — オセロゲーム (実データ)
	reversiHandler := apireversi.NewHandler(reversiRepo, idGen)
	reversiHandler.SetService(reversiService)
	reversiHandler.SetFederation(s.config.URL, deliverService, reversiFedCache, userRepo)
	reversiHandler.SetStreamPublisher(reversiPublisher)
	// #417 P3: reversi 連合対応ホストのみ Invite を送る。Federation check
	// 用の HTTP client は SSRF-safe transport + timeout を明示的に渡す
	// (既存の webfingerClient と同じ設定を流用)。
	reversiHandler.SetFederationChecker(corereversi.NewFederationChecker(
		s.redis.Default,
		s.outboundClient(10*time.Second),
	))
	// #417 P3: /match の acct 引数で未キャッシュのリモートユーザーを
	// WebFinger 経由で取り込めるようにする。ここで webfingerClient /
	// federationResolver は既存の users/show 用と同じインスタンスを再利用。
	reversiHandler.SetRemoteUserLookup(corefederation.NewRemoteUserResolver(
		webfingerClient, federationResolver, userRepo, localHost,
	))
	// Service 側にも federation 一式を注入して state 変化時に Update / Leave を
	// 配信できるようにする (#417 P1)。fedCache も Service 側で
	// session→game 解決に必要なので忘れず設定する。
	reversiService.SetFederationCache(reversiFedCache)
	reversiService.SetFederationDeliverer(deliverService)
	reversiService.SetUserRepo(userRepo)
	reversiService.SetBaseURL(s.config.URL)
	// 連合 inbox 経由の invite 受信時にも local 被招待者の reversi stream に
	// `invited` を push する (#417 P2: リアルタイム招待)。
	federationProcessor.SetReversiStreamPublisher(reversiPublisher)
	api.POST("/reversi/games", reversiHandler.Games)
	api.POST("/reversi/invitations", reversiHandler.Invitations, middleware.RequireAuth(), middleware.RequireScope("read:account"))
	api.POST("/reversi/show-game", reversiHandler.ShowGame)
	api.POST("/reversi/match", reversiHandler.Match, middleware.RequireAuth(), middleware.RequireScope("write:account"))
	api.POST("/reversi/cancel-match", reversiHandler.CancelMatch, middleware.RequireAuth(), middleware.RequireScope("write:account"))
	api.POST("/reversi/surrender", reversiHandler.Surrender, middleware.RequireAuth(), middleware.RequireScope("write:account"))
	api.POST("/reversi/verify", reversiHandler.Verify)

	// bubble-game/* — バブルゲーム (実データ)
	bubbleGameRepo := repository.NewBubbleGameRepository(s.db)
	bubbleGameHandler := apibubblegame.NewHandler(bubbleGameRepo, idGen)
	api.POST("/bubble-game/register", bubbleGameHandler.Register, middleware.RequireAuth(), middleware.RequireScope("write:account"))
	api.POST("/bubble-game/ranking", bubbleGameHandler.Ranking)

	// chat/* — Misskey v2026 チャット機能 (実データ)
	chatHandler := apichat.NewHandler(chatRepo, idGen)
	chatHandler.SetService(chatService)
	// drive/files/attached-chat-messages: chat message を pack するため chat
	// handler が処理する。file の owner-scope (moderator は任意 file 可) (#1218)。
	chatHandler.SetDriveFileRepo(driveFileRepo)
	chatHandler.SetModeratorChecker(roleService)
	chatHandler.SetUserRepo(userRepo)
	// chatRoomInvitationReceived 通知 (#1559): 招待作成 (local handler / AP service)
	// で発火し、entity 側で read 時に packed invitation へ解決する。lookup は
	// 招待削除済なら (nil,false)。chatHandler が PackInvitationByID を提供する
	// ため、notification packer (REST handler / stream) への配線はここで行う。
	chatHandler.SetInvitationNotifier(notificationHook)
	chatService.SetInvitationNotifier(notificationHook)
	notificationsHandler.SetChatInvitationLookup(chatHandler.PackInvitationByID)
	notificationPublisher.SetChatInvitationLookup(chatHandler.PackInvitationByID)
	api.POST("/drive/files/attached-chat-messages", chatHandler.AttachedChatMessages, middleware.RequireAuth(), middleware.RequireScope("read:drive"))
	api.POST("/chat/rooms/create", chatHandler.RoomsCreate, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:chat"))
	api.POST("/chat/rooms/show", chatHandler.RoomsShow, middleware.RequireAuth(), middleware.RequireScope("read:chat"))
	api.POST("/chat/rooms/update", chatHandler.RoomsUpdate, middleware.RequireAuth(), middleware.RequireScope("write:chat"))
	api.POST("/chat/rooms/delete", chatHandler.RoomsDelete, middleware.RequireAuth(), middleware.RequireScope("write:chat"))
	api.POST("/chat/rooms/owned", chatHandler.RoomsOwned, middleware.RequireAuth(), middleware.RequireScope("read:chat"))
	api.POST("/chat/rooms/joined", chatHandler.RoomsJoined, middleware.RequireAuth(), middleware.RequireScope("read:chat"))
	api.POST("/chat/rooms/leave", chatHandler.RoomsLeave, middleware.RequireAuth(), middleware.RequireScope("write:chat"))
	api.POST("/chat/rooms/mute", chatHandler.RoomsMute, middleware.RequireAuth(), middleware.RequireScope("write:chat"))
	api.POST("/chat/rooms/unmute", chatHandler.RoomsUnmute, middleware.RequireAuth(), middleware.RequireScope("write:chat"))
	api.POST("/chat/rooms/transfer-ownership", chatHandler.RoomsTransferOwnership, middleware.RequireAuth(), middleware.RequireScope("write:chat"))
	api.POST("/chat/rooms/join", chatHandler.RoomsJoin, middleware.RequireAuth(), middleware.RequireScope("write:chat"))
	api.POST("/chat/rooms/joining", chatHandler.RoomsJoining, middleware.RequireAuth(), middleware.RequireScope("read:chat"))
	api.POST("/chat/rooms/members", chatHandler.RoomsMembers, middleware.RequireAuth(), middleware.RequireScope("write:chat"))
	api.POST("/chat/rooms/invitations/create", chatHandler.InvitationsCreate, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:chat"))
	api.POST("/chat/rooms/invitations/delete", chatHandler.InvitationsDelete, middleware.RequireAuth(), middleware.RequireScope("write:chat"))
	api.POST("/chat/rooms/invitations/accept", chatHandler.InvitationsAccept, middleware.RequireAuth(), middleware.RequireScope("write:chat"))
	api.POST("/chat/rooms/invitations/reject", chatHandler.InvitationsReject, middleware.RequireAuth(), middleware.RequireScope("write:chat"))
	api.POST("/chat/rooms/invitations/ignore", chatHandler.InvitationsIgnore, middleware.RequireAuth(), middleware.RequireScope("write:chat"))
	api.POST("/chat/rooms/invitations/inbox", chatHandler.InvitationsInbox, middleware.RequireAuth(), middleware.RequireScope("read:chat"))
	api.POST("/chat/rooms/invitations/outbox", chatHandler.InvitationsOutbox, middleware.RequireAuth(), middleware.RequireScope("read:chat"))
	api.POST("/chat/rooms/members/ban", chatHandler.MembersBan, middleware.RequireAuth(), middleware.RequireScope("write:chat"))
	api.POST("/chat/rooms/members/update-membership", chatHandler.MembersUpdateMembership, middleware.RequireAuth(), middleware.RequireScope("write:chat"))
	api.POST("/chat/messages", chatHandler.Messages, middleware.RequireAuth(), middleware.RequireScope("read:chat"))
	api.POST("/chat/messages/create", chatHandler.MessagesCreate, middleware.RequireAuth(), middleware.RequireScope("write:chat"))
	api.POST("/chat/messages/create-to-user", chatHandler.MessagesCreateToUser, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:chat"))
	api.POST("/chat/messages/create-to-room", chatHandler.MessagesCreateToRoom, middleware.RequireAuth(), middleware.RequireNotMoved(), middleware.RequireScope("write:chat"))
	api.POST("/chat/messages/show", chatHandler.MessagesShow, middleware.RequireAuth(), middleware.RequireScope("read:chat"))
	api.POST("/chat/messages/update", chatHandler.MessagesUpdate, middleware.RequireAuth(), middleware.RequireScope("write:chat"))
	api.POST("/chat/messages/delete", chatHandler.MessagesDelete, middleware.RequireAuth(), middleware.RequireScope("write:chat"))
	api.POST("/chat/messages/read", chatHandler.MessagesRead, middleware.RequireAuth(), middleware.RequireScope("write:chat"))
	api.POST("/chat/messages/search", chatHandler.MessagesSearch, middleware.RequireAuth(), middleware.RequireScope("read:chat"))
	api.POST("/chat/messages/user-timeline", chatHandler.UserTimeline, middleware.RequireAuth(), middleware.RequireScope("read:chat"))
	api.POST("/chat/messages/room-timeline", chatHandler.RoomTimeline, middleware.RequireAuth(), middleware.RequireScope("read:chat"))
	api.POST("/chat/messages/react", chatHandler.ReactionsCreate, middleware.RequireAuth(), middleware.RequireScope("write:chat"))
	api.POST("/chat/messages/unreact", chatHandler.ReactionsDelete, middleware.RequireAuth(), middleware.RequireScope("write:chat"))
	api.POST("/chat/messages/reactions/create", chatHandler.ReactionsCreate, middleware.RequireAuth(), middleware.RequireScope("write:chat"))
	api.POST("/chat/messages/reactions/delete", chatHandler.ReactionsDelete, middleware.RequireAuth(), middleware.RequireScope("write:chat"))
	api.POST("/chat/history", chatHandler.History, middleware.RequireAuth(), middleware.RequireScope("read:chat"))
	api.POST("/chat/read-all", chatHandler.ReadAll, middleware.RequireAuth(), middleware.RequireScope("write:chat"))
	api.POST("/chat/unread-count", chatHandler.UnreadCount, middleware.RequireAuth(), middleware.RequireScope("read:chat"))

	// --- Phase P3: 補助公開エンドポイント ---

	// get-online-users-count — オンラインユーザー数。フロントが
	// `misskeyApiGet` で GET 呼び出ししても catchall に落ちないよう
	// 両メソッドを受ける (#421)。POST のみだと `count` が undefined になり
	// admin overview の「NaN 人」表示の原因になっていた。
	onlineUsersHandler := func(c echo.Context) error {
		count, _ := userRepo.CountOnlineUsers()
		return c.JSON(http.StatusOK, map[string]any{"count": count})
	}
	api.GET("/get-online-users-count", onlineUsersHandler)
	api.POST("/get-online-users-count", onlineUsersHandler)

	// server-info (公開版) — サーバー情報
	// frontend の server-metric widget は `misskeyApiGet` で GET 呼び出し、
	// それ以外の MkVisitorDashboard 等は POST で呼ぶため両メソッドを登録。
	serverInfoHandler := func(c echo.Context) error {
		// 公開エンドポイントは machine/cpu/mem/fs のみを返す (upstream public
		// server-info.ts)。os/node/psql/redis/net は admin 専用で未認証には出さない。
		if m, err := metaRepo.Fetch(); err == nil && m.EnableServerMachineStats {
			return c.JSON(http.StatusOK, serverstats.CollectPublic())
		}
		return c.JSON(http.StatusOK, serverstats.EmptyPublic())
	}
	api.POST("/server-info", serverInfoHandler)
	api.GET("/server-info", serverInfoHandler)

	// endpoints — 登録済みAPIエンドポイント一覧
	api.POST("/endpoints", func(c echo.Context) error {
		return c.JSON(http.StatusOK, apiEndpointNames(s.echo))
	})

	// endpoint — 指定 endpoint の情報を返す (#1695)。upstream endpoint.ts は
	// 未知 endpoint で null、既知なら { params: [{name,type}, ...] } を返す。
	// mk-go は per-endpoint の paramDef メタデータを保持しない (handler は
	// router.go の inline closure で param schema を構造化していない) ため、
	// param 名/型までは導出できない。ここでは response の shape (params は配列)
	// と未知 endpoint の null だけ upstream に揃える。params 内容の導出は
	// endpoint registry の新設が必要で本 endpoint の用途 (API introspection) に
	// 対して費用対効果が低いため非対応 (#1695 に明記)。
	api.POST("/endpoint", func(c echo.Context) error {
		var req struct {
			Endpoint string `json:"endpoint"`
		}
		if err := c.Bind(&req); err != nil || req.Endpoint == "" {
			return c.JSON(http.StatusBadRequest, apierr.InvalidParam())
		}
		// 未知 endpoint は null (upstream: ep == null → return null)。
		if !isRegisteredAPIEndpoint(s.echo, req.Endpoint) {
			return c.JSON(http.StatusOK, nil)
		}
		return c.JSON(http.StatusOK, map[string]any{"params": []any{}})
	})

	// retention — リテンション統計
	api.POST("/retention", func(c echo.Context) error {
		records, err := retentionRepo.ListRecent(30)
		if err != nil {
			return c.JSON(http.StatusOK, []any{})
		}
		out := make([]map[string]any, 0, len(records))
		for _, r := range records {
			out = append(out, map[string]any{
				"createdAt": r.CreatedAt,
				"users":     r.UsersCount,
				"data":      r.Data,
			})
		}
		return c.JSON(http.StatusOK, out)
	})

	// get-avatar-decorations — アバターデコレーション全件取得
	api.POST("/get-avatar-decorations", func(c echo.Context) error {
		var decorations []model.AvatarDecoration
		if err := s.db.Find(&decorations).Error; err != nil {
			return c.JSON(http.StatusOK, []any{})
		}
		out := make([]map[string]any, 0, len(decorations))
		for _, d := range decorations {
			out = append(out, map[string]any{
				"id":                                 d.ID,
				"name":                               d.Name,
				"description":                        d.Description,
				"url":                                d.URL,
				"roleIdsThatCanBeUsedThisDecoration": d.RoleIDs,
				// upstream Misskey #17034 (= 2026.5.0) で追加された category field
				// もここで返す。nullable なので null のままも許容。
				"category": d.Category,
			})
		}
		return c.JSON(http.StatusOK, out)
	})

	// email-address/available — メールアドレスの利用可否チェック (public)
	//
	// upstream email-address/available.ts は requireCredential:false の public
	// endpoint で、EmailService.validateEmailForAccount が reason を
	// null|'used'|'format'|'disposable'|'mx'|'smtp'|'banned'|'network'|'blacklist'
	// で返す (#1551)。mk-go は外部依存の active validation (mx/smtp/disposable) は
	// 行わず、format → used (emailVerified=true 一致) → banned の順で判定する
	// (upstream の enableActiveEmailValidation=false 相当)。
	api.POST("/email-address/available", func(c echo.Context) error {
		var req struct {
			EmailAddress string `json:"emailAddress"`
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, apierr.InvalidParam())
		}
		// upstream paramDef は emailAddress に minLength を持たないため、空文字は
		// 400 ではなく format 不正として available:false / reason:"format" を返す。
		available := true
		var reason *string
		setReason := func(r string) {
			available = false
			reason = &r
		}
		switch {
		case !coreemail.ValidateFormat(req.EmailAddress):
			setReason("format")
		default:
			// emailVerified=true の profile とだけ照合する (upstream は
			// countBy({emailVerified:true, email}))。未認証 email は重複扱いしない。
			var count int64
			s.db.Model(&model.UserProfile{}).
				Where(`"email" = ? AND "emailVerified" = ?`, req.EmailAddress, true).
				Count(&count)
			if count > 0 {
				setReason("used")
			} else {
				domain := ""
				if at := strings.IndexByte(req.EmailAddress, '@'); at >= 0 {
					domain = strings.ToLower(req.EmailAddress[at+1:])
				}
				if m, err := metaRepo.Fetch(); err == nil && m != nil && coreemail.IsBannedDomain(domain, m.BannedEmailDomains) {
					setReason("banned")
				}
			}
		}
		return c.JSON(http.StatusOK, map[string]any{
			"available": available,
			"reason":    reason,
		})
	})

	// promo/read — プロモノートの既読マーク (認証必須)
	api.POST("/promo/read", func(c echo.Context) error {
		user := middleware.GetUser(c)
		var req struct {
			NoteID string `json:"noteId"`
		}
		if err := c.Bind(&req); err != nil || req.NoteID == "" {
			return c.JSON(http.StatusBadRequest, apierr.InvalidParam())
		}
		if _, err := noteRepo.FindByID(req.NoteID); err != nil {
			return apierr.JSONNoSuchNote(c)
		}
		_ = promoReadRepo.MarkRead(&model.PromoRead{
			ID:     idGen.Generate(time.Now()),
			UserID: user.ID,
			NoteID: req.NoteID,
		})
		return c.NoContent(http.StatusNoContent)
	}, middleware.RequireAuth(), middleware.RequireScope("write:account"))

	// invite/* — 招待コード user-scope 4 endpoint。canInvite role policy gate
	// (#1020) + inviteLimit / inviteLimitCycle / inviteExpirationTime (#1029
	// PR-2) は handler 側で enforcement。handler 抽出後の unit test は
	// internal/api/invite/handler_test.go を参照。
	inviteHandler := apiinvite.NewHandler(signupTicketRepo, idGen)
	inviteHandler.SetRolePolicyProvider(roleService)
	api.POST("/invite/create", inviteHandler.Create,
		middleware.RequireAuth(),
		middleware.RequireRolePolicy(roleService, corerole.PolicyCanInvite), middleware.RequireScope("write:invite-codes"))

	api.POST("/invite/list", inviteHandler.List, middleware.RequireAuth(), middleware.RequireScope("read:invite-codes"))
	api.POST("/invite/delete", inviteHandler.Delete, middleware.RequireAuth(), middleware.RequireScope("write:invite-codes"))
	api.POST("/invite/limit", inviteHandler.Limit, middleware.RequireAuth(), middleware.RequireScope("read:invite-codes"))

	// notes (plain) — bulk note lookup by noteIds
	api.POST("/notes", notesHandler.BulkShow)

	// export-custom-emojis — 全 local custom emoji を zip (画像 + meta.json) に
	// export する非同期ジョブを enqueue する (#1217)。upstream 同様 endpoint は
	// 即 204 を返し、生成完了後に exportCompleted 通知 + drive file で受け取る。
	api.POST("/export-custom-emojis", func(c echo.Context) error {
		user := middleware.GetUser(c)
		if user == nil {
			return c.NoContent(http.StatusNoContent)
		}
		if err := s.queueClient.EnqueueExport(queue.ExportPayload{
			UserID: user.ID,
			Type:   coretransfer.ExportCustomEmojis,
		}); err != nil {
			slog.Warn("export-custom-emojis: enqueue failed", "user", user.ID, "err", err)
		}
		return c.NoContent(http.StatusNoContent)
	}, middleware.RequireAuth(), middleware.RequireSecure())

	// fetch-external-resources — external JSON resource を hash 検証付きで取得
	// (#1222)。外部 theme/plugin 等を integrity-check して返す。SSRF-safe client
	// 経由、upstream requireCredential 相当で RequireAuth を要求する。
	fetchExternalHandler := apifetchexternal.New(s.outboundClient(apifetchexternal.FetchTimeout), s.config.UserAgent)
	api.POST("/fetch-external-resources", fetchExternalHandler.Fetch, middleware.RequireAuth(), middleware.RequireSecure())

	// fetch-rss — RSS / Atom feed プロキシ。frontend RSS / RSSTicker ウィジェット
	// が GET で叩く実装になっているため Misskey TS と同じく allowGet 相当で
	// GET / POST の両方を受け付ける。
	fetchRSSHandler := apifetchrss.New(s.outboundClient(apifetchrss.FetchTimeout), s.config.UserAgent)
	api.GET("/fetch-rss", fetchRSSHandler.Fetch)
	api.POST("/fetch-rss", fetchRSSHandler.Fetch)

	// page-push — page scriptが任意のeventをpage所有者のmainに emit する。
	api.POST("/page-push", pagesHandler.PagePush, middleware.RequireAuth(), middleware.RequireSecure())

	// v2/admin/emoji/list — v2はページネーション情報付きオブジェクトを返す専用ハンドラ
	api.POST("/v2/admin/emoji/list", adminHandler.EmojiListV2, middleware.RequireRolePolicy(roleService, corerole.PolicyCanManageCustomEmojis), middleware.RequireScope("read:admin:emoji"))

	// --- その他の残りエンドポイント ---
	// test — フロントエンドのテスト用。upstream test.ts は validate 済の ps を
	// そのまま echo し、required:['required'] が未指定なら 400 を返す (#1551)。
	// default は 'hello'、nullableDefault も既定 'hello'。string / id は指定時のみ
	// 含める。explicit null と absent の区別は test 用途なので簡略化している。
	api.POST("/test", func(c echo.Context) error {
		var req struct {
			Required        *bool   `json:"required"`
			String          *string `json:"string"`
			Default         *string `json:"default"`
			NullableDefault *string `json:"nullableDefault"`
			ID              *string `json:"id"`
		}
		if err := c.Bind(&req); err != nil || req.Required == nil {
			return c.JSON(http.StatusBadRequest, apierr.InvalidParam())
		}
		out := map[string]any{"required": *req.Required}
		if req.String != nil {
			out["string"] = *req.String
		}
		out["default"] = "hello"
		if req.Default != nil {
			out["default"] = *req.Default
		}
		out["nullableDefault"] = "hello"
		if req.NullableDefault != nil {
			out["nullableDefault"] = *req.NullableDefault
		}
		if req.ID != nil {
			out["id"] = *req.ID
		}
		return c.JSON(http.StatusOK, out)
	})

	// API catchall — 意図的に 200 + 空オブジェクトを返す。未登録エンドポイントへの
	// 404 は Misskey 公式フロントの一部ページで例外を投げてしまうため、
	// 互換性優先で pass-through にしている (本家 TS Misskey と同じ運用)。
	// 実装漏れは warn ログで検知する。
	api.Any("/*", func(c echo.Context) error {
		slog.Warn("unimplemented API endpoint", "method", c.Request().Method, "path", c.Request().URL.Path)
		return c.JSON(http.StatusOK, map[string]any{})
	})

	// フロントエンドアセット配信
	// ビルド済みアセットがあれば静的配信、なければVite dev serverプロキシ
	frontendDir := frontendutil.FrontendDir()
	if _, err := os.Stat(frontendDir); err == nil {
		s.echo.Static("/vite", frontendDir)
	} else {
		s.echo.Any("/vite/*", newViteProxy("http://localhost:5173"))
	}

	// フロントエンド配布アセット (locales, fonts等) + リポジトリアセット (ai.png等)
	// Echo は同一パスに Static を 2 回登録すると上書きされるため、
	// frontendDistDir → repoAssetsDir の順にフォールバックするハンドラを使う
	frontendDistDir := frontendutil.FrontendDistDir()
	repoAssetsDir := frontendutil.RepoAssetsDir()
	s.echo.GET("/assets/*", frontendutil.AssetsHandler(frontendDistDir, repoAssetsDir))

	// twemoji SVG配信 + 30 day Cache-Control (upstream `ms('30 days')` 同等)。
	twemojiDir := frontendutil.TwemojiDir()
	if _, err := os.Stat(twemojiDir); err == nil {
		serveStaticAssetDir(s.echo, "/twemoji", twemojiDir)
	}

	// fluent-emoji PNG配信 (実績バッジ等で参照される)。upstream Misskey TS の
	// /fluent-emoji/:hex.png 経路 (ClientServerService.ts) と互換、+ 30 day
	// Cache-Control (上記 twemoji と同 helper)。
	fluentEmojiDir := frontendutil.FluentEmojiDir()
	if _, err := os.Stat(fluentEmojiDir); err == nil {
		serveStaticAssetDir(s.echo, "/fluent-emoji", fluentEmojiDir)
	}

	// client-assets配信 (バブルゲーム、フラッシュ等のフロントエンドアセット)
	clientAssetsDir := frontendutil.ClientAssetsDir()
	if _, err := os.Stat(clientAssetsDir); err == nil {
		s.echo.Static("/client-assets", clientAssetsDir)
	}

	// 静的アセット配信 (favicon, splash, icons等)
	staticDir := frontendutil.StaticDir()
	if _, err := os.Stat(staticDir); err == nil {
		s.echo.Static("/static-assets", staticDir)
		s.echo.File("/favicon.ico", filepath.Join(staticDir, "favicon.ico"))
		s.echo.File("/apple-touch-icon.png", filepath.Join(staticDir, "apple-touch-icon.png"))
		s.echo.File("/robots.txt", filepath.Join(staticDir, "robots.txt"))
	}

	// identicon — ユーザーアイコン自動生成 (meta.enableIdenticonGeneration が
	// false なら 1x1 透明 PNG にフォールバック)
	s.echo.GET("/identicon/:x", identiconHandler(metaRepo))

	// /avatar/@:acct — frontend の MkMention.vue が <img src="/avatar/@user@host">
	// で直接読みに来るので、対応する handler が無いと mention chip が
	// アイコン無しで描画される (#462)。upstream Misskey TS の同名 endpoint
	// と互換: 見つかった user の avatarUrl にリダイレクトし、未設定なら
	// identicon、未存在なら /static-assets/user-unknown.png にフォールバック。
	s.echo.GET("/avatar/@:acct", avatarHandler(userRepo, localHost))

	// /emoji/:path — frontend の MkCustomEmoji が `:emojiUrl` プロップを
	// 受け取らないとき (典型: 通知 reaction icon) に <img src="/emoji/<name>@<host>.webp">
	// で直接 fetch するので、handler が無いと SPA catchall に流れて画像が
	// 読めず fallback 画像になる (#468)。upstream Misskey TS の
	// ServerService.ts:153 と互換に: name + host を抜いて emoji table を
	// lookup → publicUrl に 302 redirect、見つからなければ ?fallback クエリ
	// 付きなら /static-assets/emoji-unknown.png、無ければ 404。
	s.echo.GET("/emoji/:path", emojiRedirectHandler(emojiRepo))

	// manifest.json — PWA用
	s.echo.GET("/manifest.json", manifestJSON(s.config, metaRepo))

	// Service Worker (sw.js) — Misskey frontend は GET /sw.js を登録しにくる。
	// SPA catchall より前に登録しないと text/html にフォールバックしてしまい
	// ブラウザが "unsupported MIME type" エラーで SW 登録を拒否する。
	swDistDir := frontendutil.SwDistDir()
	if _, err := os.Stat(filepath.Join(swDistDir, "sw.js")); err == nil {
		s.echo.File("/sw.js", filepath.Join(swDistDir, "sw.js"))
	}

	// Frontend HTML shell — SPA catchall (最後に登録)
	s.echo.GET("/*", frontend)
}

// notifReaderAdapter bridges stream.NotificationReader to
// core/notification.Service.MarkAllAsRead.
type notifReaderAdapter struct {
	svc *corenotification.Service
}

func (a *notifReaderAdapter) ReadAll(userID string) error {
	return a.svc.MarkAllAsRead(context.Background(), userID)
}

// hardMuteLookupAdapter bridges UserRepository to stream.HardMuteRulesLookup
// so the streaming Manager can attach the persisted hardMutedWords (#787) at
// connection setup. Returns nil on lookup failure / empty rule set so the
// streaming filter degrades to no-op rather than dropping the connection.
type hardMuteLookupAdapter struct {
	userRepo repository.UserRepository
}

func (a *hardMuteLookupAdapter) HardMutedWordsForUser(userID string) []byte {
	if a.userRepo == nil || userID == "" {
		return nil
	}
	profile, err := a.userRepo.FindProfileByUserID(userID)
	if err != nil || profile == nil || len(profile.HardMutedWords) == 0 {
		return nil
	}
	return []byte(profile.HardMutedWords)
}

// followingSnapshotAdapter bridges FollowingRepository to
// stream.FollowingSnapshotLookup so the streaming Manager can snapshot the
// viewer's followee list at connection setup (#1063). 戻り値は followeeID →
// withReplies の map で、home/hybrid/local timeline の reply gate に使う。
//
// 依存: `repository.FollowingRepository.ListFollowing(userID, limit, offset)`
// (`internal/repository/following.go`)。既存 method なので追加配線は不要。
//
// fetch は ListFollowing をページネーションで全件読みだす。フォロー数 N が
// 巨大な場合 (10K+) のメモリ / DB 負荷はあるが、upstream Misskey TS も同等
// な map を connection ごとに保持しているので drop-in 互換性回復のためには
// 必要なコスト。pageSize は upstream の channel-following-service と同じく
// 200 / 上限は 10000 で cap し、それ以上 follow している power user は
// snapshot から外れて escape hatch のみ動作する degrade に倒す (= 旧来の
// "全 reply drop" よりは upstream 寄り)。
//
// 改善案 (本 PR scope outside): 同一 user の WebSocket 再接続ごとに毎回
// 全 followee を fetch するので、tab 多重・短期再接続の多い production
// では DB 負荷になる。per-user LRU cache を Manager に持たせれば
// connection accept のコストを下げられる。invalidation は Following 変更
// の subscriber (#791 と同じパターン) で粒度を合わせる想定。
type followingSnapshotAdapter struct {
	repo repository.FollowingRepository
}

func (a *followingSnapshotAdapter) FollowingSnapshotForUser(userID string) map[string]bool {
	if a.repo == nil || userID == "" {
		return nil
	}
	const pageSize = 200
	const maxEntries = 10000
	snap := make(map[string]bool)
	offset := 0
	for {
		rows, err := a.repo.ListFollowing(userID, pageSize, offset)
		if err != nil {
			return snap
		}
		if len(rows) == 0 {
			return snap
		}
		for _, r := range rows {
			snap[r.FolloweeID] = r.WithReplies
			if len(snap) >= maxEntries {
				return snap
			}
		}
		if len(rows) < pageSize {
			return snap
		}
		offset += pageSize
	}
}

// muteBlockSnapshotAdapter bridges the muting / blocking / renote-muting /
// channel-muting repositories + user profile to stream.MuteBlockSnapshotLookup
// so the streaming Manager can snapshot the viewer's mute/block relationships
// at connection setup (#1711). The main / notifications channels consult it to
// drop notifications / mentions involving muted instances, muted users, users
// blocking the viewer, renote-muted authors, or muted channels.
//
// 各 set fetch は best-effort: 個別の repo error は空 set に degrade させ、
// snapshot 全体は常に non-nil で返す (= 取得できた範囲だけ filter する)。
// followingSnapshotAdapter と同じく接続ごとに 1 回読みだす connection-time
// snapshot で、live refresh は未実装 (= 再接続まで stale)。
type muteBlockSnapshotAdapter struct {
	muting        repository.MutingRepository
	blocking      repository.BlockingRepository
	renoteMuting  repository.RenoteMutingRepository
	channelMuting repository.ChannelMutingRepository
	userRepo      repository.UserRepository
}

func (a *muteBlockSnapshotAdapter) MuteBlockSnapshotForUser(userID string) *stream.MuteBlockSnapshot {
	if userID == "" {
		return nil
	}
	snap := &stream.MuteBlockSnapshot{
		Muting:         map[string]struct{}{},
		BlockingMe:     map[string]struct{}{},
		RenoteMuting:   map[string]struct{}{},
		MutedInstances: map[string]struct{}{},
		MutingChannels: map[string]struct{}{},
	}
	if a.muting != nil {
		if ids, err := a.muting.ListMuteeIDs(userID); err == nil {
			for _, id := range ids {
				snap.Muting[id] = struct{}{}
			}
		}
	}
	if a.blocking != nil {
		if ids, err := a.blocking.ListBlockerIDs(userID); err == nil {
			for _, id := range ids {
				snap.BlockingMe[id] = struct{}{}
			}
		}
	}
	if a.renoteMuting != nil {
		if ids, err := a.renoteMuting.ListMuteeIDs(userID); err == nil {
			for _, id := range ids {
				snap.RenoteMuting[id] = struct{}{}
			}
		}
	}
	if a.channelMuting != nil {
		if rows, err := a.channelMuting.ListByUser(userID); err == nil {
			for _, r := range rows {
				snap.MutingChannels[r.ChannelID] = struct{}{}
			}
		}
	}
	if a.userRepo != nil {
		if profile, err := a.userRepo.FindProfileByUserID(userID); err == nil && profile != nil && len(profile.MutedInstances) > 0 {
			var hosts []string
			if json.Unmarshal(profile.MutedInstances, &hosts) == nil {
				for _, h := range hosts {
					if h != "" {
						// host は AP canonical の lowercase で比較する
						// (timeline filter / loadMutedInstances と統一)。
						snap.MutedInstances[strings.ToLower(h)] = struct{}{}
					}
				}
			}
		}
	}
	return snap
}

// hardMutePublisherAdapter bridges PubSubService to i.HardMutePublisher so
// i/update can flush a wordmute reload event to every active streaming
// connection of the user (#791). Failures are logged but never bubbled — the
// API response should not fail just because the realtime nudge couldn't be
// delivered (clients still pick up the new rules at the next reconnect).
type hardMutePublisherAdapter struct {
	pubsub *event.PubSubService
}

func (a *hardMutePublisherAdapter) PublishHardMuteReload(userID string) {
	if a.pubsub == nil || userID == "" {
		return
	}
	payload := stream.WordMuteReloadPayload{UserID: userID}
	if err := a.pubsub.Publish(context.Background(), stream.WordMuteReloadTopic, payload); err != nil {
		slog.Warn("router: wordmute reload publish failed", "userID", userID, "err", err)
	}
}

// reactionNoteStreamAdapter bridges core/reaction.NoteStreamHook to the
// shared stream.NoteEventPublisher. Misskey TS upstream の wire format に
// 揃え、`{type: "reacted"|"unreacted", body: {reaction, emoji?, userId}}`
// を `noteStream:<noteID>` に publish する (#700)。
type reactionNoteStreamAdapter struct {
	pub *stream.NoteEventPublisher
}

func (a *reactionNoteStreamAdapter) OnReacted(noteID, userID, reaction string, emoji *corereaction.NoteStreamEmoji) {
	body := map[string]any{
		"reaction": reaction,
		"userId":   userID,
		"emoji":    nil,
	}
	if emoji != nil {
		body["emoji"] = map[string]any{"name": emoji.Name, "url": emoji.URL}
	}
	a.pub.PublishToNoteStream(noteID, "reacted", body)
}

func (a *reactionNoteStreamAdapter) OnUnreacted(noteID, userID, reaction string) {
	a.pub.PublishToNoteStream(noteID, "unreacted", map[string]any{
		"reaction": reaction,
		"userId":   userID,
	})
}

// noteDeleteStreamAdapter bridges core/note.DeleteNoteStreamHook to the
// shared stream.NoteEventPublisher (#700)。
type noteDeleteStreamAdapter struct {
	pub *stream.NoteEventPublisher
}

func (a *noteDeleteStreamAdapter) OnNoteDeleted(noteID string, deletedAt time.Time) {
	a.pub.PublishToNoteStream(noteID, "deleted", map[string]any{
		"deletedAt": deletedAt.UTC().Format(time.RFC3339Nano),
	})
}

// instanceActorSigner is the SignerProvider used by APFetcher to sign
// outgoing GETs with the instance.actor system account (#419)。
//
// 初回 Signer() で systemaccount.Fetch + keypair lookup + RSA PEM parse を
// やってから、結果 (parse 済みの *PrivateKey) を mutex 配下にキャッシュする。
// 一度成功した key は process lifetime 中に変わらない前提で無期限保持し、
// 再起動で再ロードされる。
//
// 失敗側はキャッシュしない: transient な DB glitch で起動直後に load が
// コケた時、その後ずっと unsigned-only に張り付くことを避けるため、失敗
// を `signerLoadBackoff` だけ抑制してから再試行する (#419 Devin review)。
type instanceActorSigner struct {
	sysAcct *coresystemaccount.Service
	keypair repository.UserKeypairRepository
	urls    *activitypub.URLBuilder

	mu          sync.RWMutex
	cachedKey   *activitypub.PrivateKey
	lastFailure time.Time
}

// signerLoadBackoff は load() 失敗後の再試行抑制間隔。DB が一時的に死んだ
// 時に Signer() の per-fetch で同じクエリを叩き続けないようにする。
const signerLoadBackoff = 30 * time.Second

func newInstanceActorSigner(
	svc *coresystemaccount.Service,
	kp repository.UserKeypairRepository,
	urls *activitypub.URLBuilder,
) *instanceActorSigner {
	return &instanceActorSigner{sysAcct: svc, keypair: kp, urls: urls}
}

// Signer returns the parsed instance.actor PrivateKey. corefederation.ErrNoSigner
// を返した場合 APFetcher は unsigned-only モードで継続する。
//
// double-checked locking で hot path (cache hit) を read-lock のみにし、
// 同時 fetch が DB query 待ちで直列化しないようにする (#419 Devin review)。
// slog.Warn は lock 解除後に呼んで、log handler が遅い時に他の goroutine を
// 詰まらせない (#419 Devin review)。
func (s *instanceActorSigner) Signer() (*activitypub.PrivateKey, error) {
	// Fast path: 既に load 済みなら read-lock だけで返す
	s.mu.RLock()
	if k := s.cachedKey; k != nil {
		s.mu.RUnlock()
		return k, nil
	}
	s.mu.RUnlock()

	// Slow path: load を試みる
	var (
		loaded  *activitypub.PrivateKey
		loadLog func() // nil 以外なら lock 解除後に呼ぶ Warn ログ
	)
	s.mu.Lock()
	switch {
	case s.cachedKey != nil:
		// double-check: lock 取得待ちの間に他 goroutine が load を成功させた
		loaded = s.cachedKey
	case !s.lastFailure.IsZero() && time.Since(s.lastFailure) < signerLoadBackoff:
		// 直近の失敗から十分時間が経っていない → DB spam 抑制で skip
	default:
		key, logFn := s.loadLocked()
		loadLog = logFn
		if key != nil {
			s.cachedKey = key
			s.lastFailure = time.Time{}
			loaded = key
		} else {
			s.lastFailure = time.Now()
		}
	}
	s.mu.Unlock()

	if loadLog != nil {
		loadLog()
	}
	if loaded != nil {
		return loaded, nil
	}
	return nil, corefederation.ErrNoSigner
}

// remoteStatsFetcherAdapter は corefederation.RemoteStatsFetcher を
// users.RemoteStatsFetcher interface に橋渡しする (#943)。layered architecture
// (api → core → repository) を保つため users package が federation を直接 import
// しない設計を維持する。
type remoteStatsFetcherAdapter struct {
	fetcher *corefederation.RemoteStatsFetcher
}

func (a *remoteStatsFetcherAdapter) Fetch(ctx context.Context, host, username string) *users.RemoteUserStatsView {
	stats := a.fetcher.Fetch(ctx, host, username)
	if stats == nil {
		return nil
	}
	return &users.RemoteUserStatsView{
		NotesCount:     stats.NotesCount,
		FollowersCount: stats.FollowersCount,
		FollowingCount: stats.FollowingCount,
	}
}

// loadLocked performs the actual systemaccount + keypair + PEM parse. mu を
// 既に保持している前提。Warn ログ呼び出しは caller (Signer) が lock 解除後
// に走らせるよう関数値で返す (sync 化されたログハンドラ呼び出しが他の
// goroutine をブロックしないため, #419 Devin review)。
func (s *instanceActorSigner) loadLocked() (*activitypub.PrivateKey, func()) {
	user, err := s.sysAcct.Fetch("instance")
	if err != nil || user == nil {
		return nil, func() {
			slog.Warn("instance.actor signer: system account fetch failed; AP fetches will fall back to unsigned",
				"err", err)
		}
	}
	kp, err := s.keypair.FindByUserID(user.ID)
	if err != nil || kp == nil || kp.PrivateKey == "" {
		return nil, func() {
			slog.Warn("instance.actor signer: keypair lookup failed; AP fetches will fall back to unsigned",
				"userId", user.ID, "err", err)
		}
	}
	key, err := activitypub.NewPrivateKey(s.urls.UserKeyURI(user.ID), kp.PrivateKey)
	if err != nil {
		return nil, func() {
			slog.Warn("instance.actor signer: PEM parse failed; AP fetches will fall back to unsigned",
				"userId", user.ID, "err", err)
		}
	}
	return key, nil
}
