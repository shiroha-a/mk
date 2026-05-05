package server

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"io"
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
	apifetchrss "github.com/shiroha-a/mk/internal/api/fetchrss"
	apiflash "github.com/shiroha-a/mk/internal/api/flash"
	"github.com/shiroha-a/mk/internal/api/following"
	apigallery "github.com/shiroha-a/mk/internal/api/gallery"
	apihashtags "github.com/shiroha-a/mk/internal/api/hashtags"
	"github.com/shiroha-a/mk/internal/api/i"
	"github.com/shiroha-a/mk/internal/api/inbox"
	"github.com/shiroha-a/mk/internal/api/meta"
	"github.com/shiroha-a/mk/internal/api/mute"
	"github.com/shiroha-a/mk/internal/api/nodeinfo"
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
	coreemojiimport "github.com/shiroha-a/mk/internal/core/emojiimport"
	"github.com/shiroha-a/mk/internal/core/event"
	corefederation "github.com/shiroha-a/mk/internal/core/federation"
	coreflash "github.com/shiroha-a/mk/internal/core/flash"
	corefollowing "github.com/shiroha-a/mk/internal/core/following"
	corehashtag "github.com/shiroha-a/mk/internal/core/hashtag"
	coreinstance "github.com/shiroha-a/mk/internal/core/instance"
	coremediaproxy "github.com/shiroha-a/mk/internal/core/mediaproxy"
	coremodlog "github.com/shiroha-a/mk/internal/core/moderationlog"
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
	instanceRepo := repository.NewInstanceRepository(s.db)
	// instance.{followersCount,followingCount} はリアルタイム incremental
	// 維持の hook が未実装で常に 0 → admin/overview の federation pie chart
	// が空になる。起動時に `following` テーブルから一回 backfill する
	// (#421)。失敗は警告だけで起動継続。
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
	signupService := coresignup.NewService(userRepo, metaRepo, idGen)
	// ActivityPub 配信のためにローカルユーザーは RSA 鍵対を必要とする。
	signupService.SetKeypairRepo(keypairRepo)
	noteCreateService := corenote.NewCreateService(noteRepo, pollRepo, idGen, followingRepo)
	// Phase 7-1 follow-up (#254): 本家互換の残存 error 検出ロジック用依存を注入。
	// Setter経由なのでテストでは任意に省略可能。既存の同repository変数を再利用。
	noteCreateService.SetBlockingRepo(blockingRepo)
	noteCreateService.SetDriveFileRepo(driveFileRepo)
	noteCreateService.SetMetaRepo(metaRepo)
	noteCreateService.SetChannelRepo(channelRepo)
	noteDeleteService := corenote.NewDeleteService(noteRepo)
	noteQueryService := corenote.NewQueryService(noteRepo, followingRepo)
	noteQueryService.SetFavoriteRepo(noteFavoriteRepo)
	noteQueryService.SetThreadMutingRepo(noteThreadMutingRepo)
	userService := coreuser.NewService(userRepo, noteRepo, piningRepo, idGen)
	// /api/i/update の avatarId / bannerId 経路 (#467) で drive_file の
	// 所有権検証 + URL コピーが必要なため、driveFileRepo を配線する。
	userService.SetDriveFileRepository(driveFileRepo)
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
	noteCreateService.SetFanoutHook(timelineFanoutHook)
	// #379: Delete (inbound activity / local notes/delete どちらも) で
	// Redis fanout timelines から note ID を LREM するための hook。
	noteDeleteService.SetTimelineHook(timelineFanoutHook)

	// Channels (Phase 4.2)
	channelService := corechannel.NewService(channelRepo, channelFollowingRepo, noteRepo, idGen)
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
	// Phase 7-2 follow-up (#271): antenna 着信時に所有者の unread row を
	// 作成して /api/i の hasUnreadAntenna に反映する。
	antennaNoteUnreadRepo := repository.NewAntennaNoteUnreadRepository(s.db)
	antennaService.SetUnreadRepo(antennaNoteUnreadRepo)
	noteCreateService.SetAntennaHook(coreantenna.NewNoteCreateHook(antennaService))

	// Clips (Phase 4.4)
	clipService := coreclip.NewService(clipRepo, clipNoteRepo, noteRepo, idGen)

	// Pages / Flash (Phase 4.5)
	pageService := corepage.NewService(pageRepo, pageLikeRepo, idGen)
	flashService := coreflash.NewService(flashRepo, flashLikeRepo, idGen)

	// Reactions
	reactionService := corereaction.NewService(noteRepo, reactionRepo, emojiRepo, followingRepo, idGen)

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
	noteCreateService.SetNotificationHook(notificationHook)
	noteCreateService.SetUserRepo(userRepo)
	followingService.SetNotificationHook(notificationHook)
	reactionService.SetNotificationHook(notificationHook)

	// Web Push (Phase 9.3): 通知作成後にサブスクライバーへpush配信する。
	webPushService := corewebpush.NewService(s.queueClient)
	notificationHook.SetWebPushPublisher(webPushService)
	notificationHook.SetPackers(
		corewebpush.NewUserRepoPacker(userRepo),
		corewebpush.NewNoteRepoPacker(noteRepo, idGen),
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
	s.registerShutdownHook(func() { pollExpiryCancel() })
	// pollVoted を note の stream topic に publish して subscribe 中の
	// frontend (note 詳細 / timeline) が reload なしで count を更新できる
	// ようにする (#690)。streamPubSub は下方で生成されるため遅延配線。

	// Drive storage (Phase 4.9: Meta に基づいて Local / S3 を切り替え)
	var driveStorage coredrive.Storage
	if serverMeta, err := metaRepo.Fetch(); err == nil {
		driveStorage = coredrive.NewStorageFromMeta(serverMeta, "./drive-files", s.config.DriveURL)
	} else {
		driveStorage = coredrive.NewLocalStorage("./drive-files", s.config.DriveURL)
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
	s.registerShutdownHook(func() { instanceTouchBuffer.Close() })

	// AP delivery: DeliverService + フック登録 + asynq processor 登録
	deliverService := corefederation.NewDeliverService(s.queueClient, userRepo, followingRepo, keypairRepo, apURLs)
	deliverService.SetHostBlockChecker(instanceService)

	// poll federation hook: ローカル user が remote poll に投票したら AP Note
	// (with name + inReplyTo) を author の inbox に配信する (#690)。
	pollService.SetFederationHook(corefederation.NewPollDeliveryHook(apRenderer, deliverService, userRepo, apURLs, idGen))

	// Relay 連携 (#161): relay actor system account + relay.Service。
	// relay.Service は AP delivery を必要とするので deliverService 構築の
	// 直後でセットアップする。admin/relays ハンドラへ注入し、Accept/Reject
	// inbox 処理で status を更新できるように processor にも渡す。
	sysAcctSvc := coresystemaccount.NewService(userRepo, keypairRepo, repository.NewSystemAccountRepository(s.db), idGen)
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
	noteDeleteHook := corefederation.NewNoteDeleteDeliveryHook(deliverService, apRenderer, apURLs)
	noteDeleteHook.SetUserRepo(userRepo)
	noteDeleteService.SetFederationHook(noteDeleteHook)
	deliverProcessor := processors.NewDeliverProcessor(apClient)
	// 配信結果に応じて instance.isNotResponding を更新する
	deliverProcessor.SetResponseHook(instanceService)
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
	// chartHook は後段で SetChartHook 注入する (deliverProcessor と同じ pattern)。
	s.queueServer.Handle(queue.TaskTypeInbox, inboxProcessor.Handle)

	// Remote notes cleaning (issue #46)
	if cleanMeta, err := metaRepo.Fetch(); err == nil {
		cleanCfg := processors.CleanRemoteNotesConfig{
			Enabled:              cleanMeta.EnableRemoteNotesCleaning,
			ExpiryDays:           cleanMeta.RemoteNotesCleaningExpiryDaysForEachNotes,
			MaxProcessingMinutes: cleanMeta.RemoteNotesCleaningMaxProcessingDurationInMinutes,
		}
		cleanProcessor := processors.NewCleanRemoteNotesProcessor(noteRepo, cleanCfg)
		s.queueServer.Handle(queue.TaskTypeCleanRemoteNotes, cleanProcessor.Handle)
		// 6時間ごとに cleaning job をエンキューする。
		if cleanCfg.Enabled {
			go func() {
				ticker := time.NewTicker(6 * time.Hour)
				defer ticker.Stop()
				_ = s.queueClient.EnqueueCleanRemoteNotes()
				for range ticker.C {
					_ = s.queueClient.EnqueueCleanRemoteNotes()
				}
			}()
		}
	}

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

	// Hashtag service: ノート作成 (local / federation 両経路) で hashtag table の
	// mentionedUsersCount / mentionedUserIds を更新する (#680)。/api/hashtags/list
	// 等の trends ranking はこの集計を見るので、未配線だと空集合のままになる。
	hashtagService := corehashtag.NewService(hashtagRepo, idGen)
	noteCreateService.SetHashtagHook(hashtagService)
	federationResolver.SetHashtagHook(hashtagService)

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
			result = append(result, entity.PackUserDetailed(u, profile))
		}
		return c.JSON(http.StatusOK, result)
	})

	// Pinned users (public)
	api.POST("/pinned-users", func(c echo.Context) error {
		m, err := metaRepo.Fetch()
		if err != nil || len(m.PinnedUsers) == 0 {
			return c.JSON(http.StatusOK, []any{})
		}
		var result []entity.UserLite
		for _, username := range m.PinnedUsers {
			if u, err := userRepo.FindByUsernameLower(username, nil); err == nil {
				result = append(result, entity.PackUserLite(u))
			}
		}
		if result == nil {
			result = []entity.UserLite{}
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
	// emailRequiredForSignup フローの確認メール送信。SMTP infra が enabled の
	// ときのみ closure を渡す (reset-password と同じパターン)。
	if signupSmtpMeta, err := metaRepo.Fetch(); err == nil && signupSmtpMeta.EnableEmail && signupSmtpMeta.Email != nil && signupSmtpMeta.SmtpHost != nil {
		fromAddr := *signupSmtpMeta.Email
		host := *signupSmtpMeta.SmtpHost
		port := 587
		if signupSmtpMeta.SmtpPort != nil {
			port = *signupSmtpMeta.SmtpPort
		}
		smtpUser, smtpPass := signupSmtpMeta.SmtpUser, signupSmtpMeta.SmtpPass
		signupHandler.SetEmailSender(s.config.URL, func(to string, msg miscsmtp.Message) {
			miscsmtp.SendMessage(host, port, smtpUser, smtpPass, fromAddr, to, msg, miscsmtp.Options{ProxyURL: s.config.ProxySmtp})
		})
	}
	api.POST("/signup", signupHandler.Signup)
	api.POST("/signup-pending", signupHandler.SignupPending)

	// Username availability check (フロントエンドの signup フォームが呼ぶ)
	api.POST("/username/available", func(c echo.Context) error {
		var req struct {
			Username string `json:"username"`
		}
		if err := c.Bind(&req); err != nil || req.Username == "" {
			return c.JSON(http.StatusOK, map[string]any{"available": false})
		}
		_, err := userRepo.FindByUsernameLower(strings.ToLower(req.Username), nil)
		available := err != nil
		return c.JSON(http.StatusOK, map[string]any{"available": available})
	})

	// Signin (Phase 6)
	userIPRepo := repository.NewUserIPRepository(s.db)
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
	api.POST("/signin", signinHandler.Signin)
	api.POST("/signin-flow", signinHandler.SigninFlow)
	api.POST("/signin-with-passkey", signinHandler.SigninWithPasskey)

	// パスワードリセット (認証不要)
	resetReqRepo := repository.NewPasswordResetRequestRepository(s.db)
	resetHandler := apiresetpassword.NewHandler(userRepo, resetReqRepo, idGen)
	resetHandler.SetServerURL(s.config.URL)
	if smtpMeta2, err := metaRepo.Fetch(); err == nil && smtpMeta2.EnableEmail && smtpMeta2.Email != nil && smtpMeta2.SmtpHost != nil {
		fromAddr := *smtpMeta2.Email
		host := *smtpMeta2.SmtpHost
		port := 587
		if smtpMeta2.SmtpPort != nil {
			port = *smtpMeta2.SmtpPort
		}
		smtpUser, smtpPass := smtpMeta2.SmtpUser, smtpMeta2.SmtpPass
		resetHandler.SetEmailSender(func(to string, msg miscsmtp.Message) {
			miscsmtp.SendMessage(host, port, smtpUser, smtpPass, fromAddr, to, msg, miscsmtp.Options{ProxyURL: s.config.ProxySmtp})
		})
	}
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
	notesHandler.SetDriveFileRepo(driveFileRepo)
	notesHandler.SetNoteReactionRepo(reactionRepo)
	notesHandler.SetChannelRepo(channelRepo)
	notesHandler.SetChannelMutingRepo(channelMutingRepo)
	notesHandler.SetInstanceRepo(instanceRepo)
	notesHandler.SetEmojiRepo(emojiRepo)
	notesHandler.SetReactionReader(reactionCountWriter)
	notesHandler.SetDriveFolderRepo(driveFolderRepo)
	notesHandler.SetUserRepo(userRepo)
	notesHandler.SetUserListRepo(userListRepo)
	if m, err := metaRepo.Fetch(); err == nil {
		notesHandler.SetUGCVisibility(m.UgcVisibilityForVisitor)
		if m.DeeplAuthKey != nil && *m.DeeplAuthKey != "" {
			notesHandler.SetTranslator(coretranslate.NewDeepL(*m.DeeplAuthKey, m.DeeplIsPro, s.outboundClient(10*time.Second)))
		}
	}
	api.POST("/notes/create", notesHandler.Create, middleware.RequireAuth())
	api.POST("/notes/show", notesHandler.Show)
	api.POST("/notes/delete", notesHandler.Delete, middleware.RequireAuth())
	api.POST("/notes/renotes", notesHandler.Renotes)
	api.POST("/notes/replies", notesHandler.Replies)
	api.POST("/notes/children", notesHandler.Children)
	api.POST("/notes/conversation", notesHandler.Conversation)
	api.POST("/notes/search", notesHandler.Search)
	api.POST("/notes/state", notesHandler.State, middleware.RequireAuth())
	api.POST("/notes/timeline", notesHandler.Timeline, middleware.RequireAuth())
	api.POST("/notes/local-timeline", notesHandler.LocalTimeline)
	api.POST("/notes/global-timeline", notesHandler.GlobalTimeline)
	api.POST("/notes/hybrid-timeline", notesHandler.HybridTimeline, middleware.RequireAuth())
	api.POST("/notes/reactions", notesHandler.Reactions)
	api.GET("/notes/reactions", notesHandler.Reactions)
	api.POST("/notes/reactions/create", notesHandler.ReactionsCreate, middleware.RequireAuth())
	api.POST("/notes/reactions/delete", notesHandler.ReactionsDelete, middleware.RequireAuth())
	api.POST("/notes/polls/vote", notesHandler.PollsVote, middleware.RequireAuth())
	// Notes extra endpoints (Phase 6)
	notesHandler.SetFavoriteRepo(noteFavoriteRepo)
	api.POST("/notes/favorites/create", notesHandler.FavoritesCreate, middleware.RequireAuth())
	api.POST("/notes/favorites/delete", notesHandler.FavoritesDelete, middleware.RequireAuth())
	api.POST("/notes/featured", notesHandler.Featured)
	api.GET("/notes/featured", notesHandler.Featured)
	api.POST("/notes/unrenote", notesHandler.Unrenote, middleware.RequireAuth())
	api.POST("/notes/mentions", notesHandler.Mentions, middleware.RequireAuth())
	api.POST("/notes/user-list-timeline", notesHandler.UserListTimeline, middleware.RequireAuth())
	api.POST("/notes/search-by-tag", notesHandler.SearchByTag)
	api.POST("/notes/clips", notesHandler.Clips)
	api.POST("/notes/translate", notesHandler.Translate)
	api.POST("/notes/show-partial-bulk", notesHandler.ShowPartialBulk)
	// notes/drafts + notes/thread-muting + notes/polls/recommendation (実データ)
	notesHandler.SetDraftRepo(repository.NewNoteDraftRepository(s.db))
	api.POST("/notes/drafts/list", notesHandler.DraftsList, middleware.RequireAuth())
	api.POST("/notes/drafts/create", notesHandler.DraftsCreate, middleware.RequireAuth())
	api.POST("/notes/drafts/update", notesHandler.DraftsUpdate, middleware.RequireAuth())
	api.POST("/notes/drafts/delete", notesHandler.DraftsDelete, middleware.RequireAuth())
	api.POST("/notes/drafts/count", notesHandler.DraftsCount, middleware.RequireAuth())
	api.POST("/notes/thread-muting/create", notesHandler.ThreadMutingCreate, middleware.RequireAuth())
	api.POST("/notes/thread-muting/delete", notesHandler.ThreadMutingDelete, middleware.RequireAuth())
	api.POST("/notes/polls/recommendation", notesHandler.PollsRecommendation)

	// Users endpoints
	usersHandler := users.NewHandler(userService, followingService, noteRepo, idGen)
	usersHandler.SetChartHook(chartHooks)
	usersHandler.SetPiningRepo(piningRepo)
	usersHandler.SetFollowingRepo(followingRepo)
	usersHandler.SetBlockingRepo(blockingRepo)
	usersHandler.SetMutingRepo(mutingRepo)
	usersHandler.SetRenoteMutingRepo(renoteMutingRepo)
	usersHandler.SetFollowRequestRepo(followRequestRepo)
	usersHandler.SetInstanceRepo(instanceRepo)
	usersHandler.SetEmojiRepo(emojiRepo)
	usersHandler.SetReactionReader(reactionCountWriter)
	usersHandler.SetClipRepo(clipRepo)
	usersHandler.SetFlashRepo(flashRepo)
	usersHandler.SetGalleryRepo(repository.NewGalleryRepository(s.db))
	usersHandler.SetPageRepo(pageRepo)
	usersHandler.SetNoteFieldResolver(noteFieldResolver)
	api.POST("/users/show", usersHandler.Show)
	api.POST("/users/search", usersHandler.Search)
	api.POST("/users/notes", usersHandler.Notes)
	api.POST("/users/followers", usersHandler.Followers)
	api.POST("/users/following", usersHandler.Following)
	api.POST("/users/relation", usersHandler.Relation, middleware.RequireAuth())
	api.POST("/users/report-abuse", usersHandler.ReportAbuse, middleware.RequireAuth())
	api.POST("/users/reactions", usersHandler.Reactions)
	api.POST("/users/featured-notes", usersHandler.FeaturedNotes)
	api.POST("/users/search-by-username-and-host", usersHandler.SearchByUsernameAndHost)
	api.POST("/users/update-memo", usersHandler.UpdateMemo, middleware.RequireAuth())
	usersHandler.SetAbuseRepo(repository.NewAbuseReportRepository(s.db))
	usersHandler.SetMemoRepo(repository.NewUserMemoRepository(s.db))
	usersHandler.SetUserListFavoriteRepo(userListFavoriteRepo)
	usersHandler.SetUserListRepo(userListRepo)
	// Phase 7.3: users/* 完全化 (実データハンドラ)
	api.POST("/users/achievements", usersHandler.Achievements)
	api.POST("/users/clips", usersHandler.Clips)
	api.POST("/users/flashs", usersHandler.Flashs)
	api.POST("/users/gallery/posts", usersHandler.GalleryPosts)
	api.POST("/users/pages", usersHandler.Pages)
	api.POST("/users/get-frequently-replied-users", usersHandler.GetFrequentlyRepliedUsers)
	api.POST("/users/get-following-users-by-birthday", usersHandler.GetFollowingUsersByBirthday, middleware.RequireAuth())
	api.POST("/users/recommendation", usersHandler.UserRecommendation, middleware.RequireAuth())
	api.POST("/users/lists/get-memberships", usersHandler.ListsGetMemberships, middleware.RequireAuth())
	api.POST("/users/lists/create-from-public", usersHandler.ListsCreateFromPublic, middleware.RequireAuth())
	api.POST("/users/lists/favorite", usersHandler.ListsFavorite, middleware.RequireAuth())
	api.POST("/users/lists/unfavorite", usersHandler.ListsUnfavorite, middleware.RequireAuth())
	api.POST("/users/lists/update", usersHandler.ListsUpdate, middleware.RequireAuth())
	api.POST("/users/lists/update-membership", usersHandler.ListsUpdateMembership, middleware.RequireAuth())

	// Account endpoints
	registryRepo := repository.NewRegistryRepository(s.db)
	iHandler := i.NewHandler(userService, idGen)
	iHandler.SetRoleProvider(roleService)
	iHandler.SetRegistryRepo(registryRepo)
	iHandler.SetMetaRepo(metaRepo)
	iHandler.SetServerURL(s.config.URL)
	// i/update-email の verifymail / truemail SaaS 呼び出しも SSRF-safe
	// transport + forward proxy 経由にする (#638)。
	iHandler.SetEmailValidationClient(s.outboundClient(10 * time.Second))
	// SMTP メール送信を i/update-email 用に注入する。meta の SMTP 設定に従う。
	if smtpMeta, err := metaRepo.Fetch(); err == nil && smtpMeta.EnableEmail && smtpMeta.Email != nil && smtpMeta.SmtpHost != nil {
		fromAddr := *smtpMeta.Email
		host := *smtpMeta.SmtpHost
		port := 587
		if smtpMeta.SmtpPort != nil {
			port = *smtpMeta.SmtpPort
		}
		smtpUser, smtpPass := smtpMeta.SmtpUser, smtpMeta.SmtpPass
		iHandler.SetEmailSender(func(to string, msg miscsmtp.Message) {
			miscsmtp.SendMessage(host, port, smtpUser, smtpPass, fromAddr, to, msg, miscsmtp.Options{ProxyURL: s.config.ProxySmtp})
		})
	}
	if webauthnSvc != nil {
		iHandler.SetWebAuthn(webauthnSvc, userSecurityKeyRepo)
	}
	iHandler.SetSigninRepo(signinRepo)
	// P4-6 (#166): i/authorized-apps, i/revoke-token, i/gallery/*, i/page-likes
	iHandler.SetAccessTokenRepo(repository.NewAccessTokenRepository(s.db))
	iHandler.SetGalleryRepo(repository.NewGalleryRepository(s.db))
	iHandler.SetPageLikeRepo(pageLikeRepo)
	// P4-6 followup (#174): i/move は federation resolver + deliverService を使って
	// 宛先 actor 解決 → alsoKnownAs 検証 → MovedTo 書き込み → Move activity 配送。
	iHandler.SetAccountMover(coremove.NewService(userRepo, followingRepo, apURLs, apRenderer, federationResolver, deliverService))
	api.POST("/i", iHandler.Me, middleware.RequireAuth())
	api.POST("/i/update", iHandler.Update, middleware.RequireAuth())
	api.POST("/i/pin", iHandler.Pin, middleware.RequireAuth())
	api.POST("/i/unpin", iHandler.Unpin, middleware.RequireAuth())
	api.POST("/i/registry/get", iHandler.RegistryGet, middleware.RequireAuth())
	api.POST("/i/registry/set", iHandler.RegistrySet, middleware.RequireAuth())
	api.POST("/i/registry/get-all", iHandler.RegistryGetAll, middleware.RequireAuth())
	api.POST("/i/registry/keys-with-type", iHandler.RegistryKeysWithType, middleware.RequireAuth())
	api.POST("/i/registry/remove", iHandler.RegistryRemove, middleware.RequireAuth())
	api.POST("/i/change-password", iHandler.ChangePassword, middleware.RequireAuth())
	api.POST("/i/delete-account", iHandler.DeleteAccount, middleware.RequireAuth())
	api.POST("/i/favorites", iHandler.Favorites, middleware.RequireAuth())
	api.POST("/i/regenerate-token", iHandler.RegenerateToken, middleware.RequireAuth())
	iHandler.SetFavoriteRepo(noteFavoriteRepo)
	// Phase 7-2 (#244): /api/i の未読系フィールドを実クエリ化。
	iHandler.SetNotificationService(notificationService)
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
	iHandler.SetNoteFieldResolver(noteFieldResolver)
	// announcementRepoは後続で構築されるため SetupAdditional() 相当の順序依存があるが、
	// 現状 announcementRepo := ... の行がここより後にあるため下で wire する。

	// i/export-* and i/import-* (Phase 9.4)
	iHandler.SetTransferEnqueuer(s.queueClient)
	api.POST("/i/export-notes", iHandler.ExportNotes, middleware.RequireAuth())
	api.POST("/i/export-following", iHandler.ExportFollowing, middleware.RequireAuth())
	api.POST("/i/export-blocking", iHandler.ExportBlocking, middleware.RequireAuth())
	api.POST("/i/export-mute", iHandler.ExportMute, middleware.RequireAuth())
	api.POST("/i/export-favorites", iHandler.ExportFavorites, middleware.RequireAuth())
	api.POST("/i/export-user-lists", iHandler.ExportUserLists, middleware.RequireAuth())
	api.POST("/i/export-antennas", iHandler.ExportAntennas, middleware.RequireAuth())
	api.POST("/i/export-clips", iHandler.ExportClips, middleware.RequireAuth())
	api.POST("/i/import-following", iHandler.ImportFollowing, middleware.RequireAuth())
	api.POST("/i/import-blocking", iHandler.ImportBlocking, middleware.RequireAuth())
	api.POST("/i/import-muting", iHandler.ImportMuting, middleware.RequireAuth())
	api.POST("/i/import-user-lists", iHandler.ImportUserLists, middleware.RequireAuth())
	api.POST("/i/import-antennas", iHandler.ImportAntennas, middleware.RequireAuth())

	// i/webhooks/* — Webhook管理 (実データ)
	webhookHandler := apiwebhooks.NewHandler(webhookRepo, idGen)
	webhookHandler.SetDispatcher(webhookService)
	api.POST("/i/webhooks/create", webhookHandler.Create, middleware.RequireAuth())
	api.POST("/i/webhooks/list", webhookHandler.List, middleware.RequireAuth())
	api.POST("/i/webhooks/show", webhookHandler.Show, middleware.RequireAuth())
	api.POST("/i/webhooks/update", webhookHandler.Update, middleware.RequireAuth())
	api.POST("/i/webhooks/delete", webhookHandler.Delete, middleware.RequireAuth())
	api.POST("/i/webhooks/test", webhookHandler.Test, middleware.RequireAuth())

	// Notifications endpoints
	notificationsHandler := notifications.NewHandler(notificationService, idGen)
	notificationsHandler.SetRepos(userRepo, noteRepo)
	notificationsHandler.SetFollowRequestRepo(followRequestRepo)
	notificationsHandler.SetInstanceRepo(instanceRepo)
	notificationsHandler.SetEmojiRepo(emojiRepo)
	api.POST("/i/notifications", notificationsHandler.Show, middleware.RequireAuth())
	api.POST("/i/notifications-grouped", notificationsHandler.Show, middleware.RequireAuth())
	api.POST("/notifications/mark-all-as-read", notificationsHandler.MarkAllAsRead, middleware.RequireAuth())
	api.POST("/notifications/create", notificationsHandler.Create, middleware.RequireAuth())
	api.POST("/notifications/flush", notificationsHandler.Flush, middleware.RequireAuth())
	api.POST("/notifications/test-notification", notificationsHandler.TestNotification, middleware.RequireAuth())

	// i/* ハンドラ
	api.POST("/i/claim-achievement", iHandler.ClaimAchievement, middleware.RequireAuth())
	api.POST("/i/apps", iHandler.Apps, middleware.RequireAuth())
	api.POST("/i/authorized-apps", iHandler.AuthorizedApps, middleware.RequireAuth())
	api.POST("/i/signin-history", iHandler.SigninHistory, middleware.RequireAuth())
	api.POST("/i/revoke-token", iHandler.RevokeToken, middleware.RequireAuth())
	api.POST("/i/update-email", iHandler.UpdateEmail, middleware.RequireAuth())
	api.POST("/verify-email", iHandler.VerifyEmail)
	api.POST("/i/move", iHandler.Move, middleware.RequireAuth())
	api.POST("/i/2fa/register", iHandler.TwoFARegister, middleware.RequireAuth())
	api.POST("/i/2fa/done", iHandler.TwoFADone, middleware.RequireAuth())
	api.POST("/i/2fa/unregister", iHandler.TwoFAUnregister, middleware.RequireAuth())
	api.POST("/i/2fa/register-key", iHandler.TwoFARegisterKey, middleware.RequireAuth())
	api.POST("/i/2fa/key-done", iHandler.TwoFAKeyDone, middleware.RequireAuth())
	api.POST("/i/2fa/remove-key", iHandler.TwoFARemoveKey, middleware.RequireAuth())
	api.POST("/i/2fa/update-key", iHandler.TwoFAUpdateKey, middleware.RequireAuth())
	api.POST("/i/2fa/password-less", iHandler.TwoFAPasswordLess, middleware.RequireAuth())
	api.POST("/i/gallery/likes", iHandler.GalleryLikes, middleware.RequireAuth())
	api.POST("/i/gallery/posts", iHandler.GalleryPosts, middleware.RequireAuth())
	api.POST("/i/page-likes", iHandler.PageLikes, middleware.RequireAuth())
	api.POST("/i/registry/get-detail", iHandler.RegistryGetDetail, middleware.RequireAuth())
	api.POST("/i/registry/keys", iHandler.RegistryKeys, middleware.RequireAuth())
	api.POST("/i/registry/scopes-with-domain", iHandler.RegistryScopesWithDomain, middleware.RequireAuth())

	// i/export-* — データエクスポート (asynqキューにエンキュー)
	for _, exportType := range []string{
		"notes", "following", "blocking", "mute", "favorites", "user-lists", "antennas", "clips",
	} {
		et := exportType
		api.POST("/i/export-"+et, func(c echo.Context) error {
			user := middleware.GetUser(c)
			if s.queueClient != nil {
				_ = s.queueClient.EnqueueExport(queue.ExportPayload{UserID: user.ID, Type: et})
			}
			return c.NoContent(http.StatusNoContent)
		}, middleware.RequireAuth())
	}
	// i/import-* — データインポート (asynqキューにエンキュー)
	for _, importType := range []string{
		"following", "blocking", "muting", "user-lists", "antennas",
	} {
		it := importType
		api.POST("/i/import-"+it, func(c echo.Context) error {
			user := middleware.GetUser(c)
			if s.queueClient != nil {
				_ = s.queueClient.EnqueueImport(queue.ImportPayload{UserID: user.ID, Type: it})
			}
			return c.NoContent(http.StatusNoContent)
		}, middleware.RequireAuth())
	}

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
	api.POST("/gallery/featured", galleryHandler.Featured)
	api.POST("/gallery/popular", galleryHandler.Popular)
	api.POST("/gallery/posts", galleryHandler.Posts)
	api.POST("/gallery/posts/create", galleryHandler.PostsCreate, middleware.RequireAuth())
	api.POST("/gallery/posts/show", galleryHandler.PostsShow)
	api.POST("/gallery/posts/delete", galleryHandler.PostsDelete, middleware.RequireAuth())
	api.POST("/gallery/posts/update", galleryHandler.PostsUpdate, middleware.RequireAuth())
	api.POST("/gallery/posts/like", galleryHandler.PostsLike, middleware.RequireAuth())
	api.POST("/gallery/posts/unlike", galleryHandler.PostsUnlike, middleware.RequireAuth())

	// Blocking endpoints
	blockingHandler := blocking.NewHandler(blockingService, userRepo, idGen)
	api.POST("/blocking/create", blockingHandler.Create, middleware.RequireAuth())
	api.POST("/blocking/delete", blockingHandler.Delete, middleware.RequireAuth())
	api.POST("/blocking/list", blockingHandler.List, middleware.RequireAuth())

	// Mute endpoints
	muteHandler := mute.NewHandler(mutingService, userRepo, idGen)
	api.POST("/mute/create", muteHandler.Create, middleware.RequireAuth())
	api.POST("/mute/delete", muteHandler.Delete, middleware.RequireAuth())
	api.POST("/mute/list", muteHandler.List, middleware.RequireAuth())

	// Renote mute endpoints
	renoteMuteHandler := renotemute.NewHandler(renoteMutingService, userRepo, idGen)
	api.POST("/renote-mute/create", renoteMuteHandler.Create, middleware.RequireAuth())
	api.POST("/renote-mute/delete", renoteMuteHandler.Delete, middleware.RequireAuth())
	api.POST("/renote-mute/list", renoteMuteHandler.List, middleware.RequireAuth())

	// Drive endpoints
	driveHandler := drive.NewHandler(driveService, idGen)
	driveHandler.SetRepos(driveFileRepo, driveFolderRepo, noteRepo)
	driveHandler.SetUserRepo(userRepo)
	driveHandler.SetInstanceRepo(instanceRepo)
	driveHandler.SetEmojiRepo(emojiRepo)
	driveHandler.SetReactionReader(reactionCountWriter)
	driveHandler.SetNoteFieldResolver(noteFieldResolver)
	api.POST("/drive", driveHandler.Usage, middleware.RequireAuth())
	api.POST("/drive/files", driveHandler.FilesList, middleware.RequireAuth())
	api.POST("/drive/files/create", driveHandler.FilesCreate, middleware.RequireAuth())
	api.POST("/drive/files/show", driveHandler.FilesShow, middleware.RequireAuth())
	api.POST("/drive/files/update", driveHandler.FilesUpdate, middleware.RequireAuth())
	api.POST("/drive/files/delete", driveHandler.FilesDelete, middleware.RequireAuth())
	api.POST("/drive/files/find-by-hash", driveHandler.FilesFindByHash, middleware.RequireAuth())
	api.POST("/drive/folders/create", driveHandler.FoldersCreate, middleware.RequireAuth())
	api.POST("/drive/folders/show", driveHandler.FoldersShow, middleware.RequireAuth())
	api.POST("/drive/folders/update", driveHandler.FoldersUpdate, middleware.RequireAuth())
	api.POST("/drive/folders/delete", driveHandler.FoldersDelete, middleware.RequireAuth())
	api.POST("/drive/folders", driveHandler.FoldersList, middleware.RequireAuth())
	api.POST("/drive/folders/find", driveHandler.FoldersFind, middleware.RequireAuth())
	api.POST("/drive/files/find", driveHandler.FilesFind, middleware.RequireAuth())
	api.POST("/drive/files/check-existence", driveHandler.FilesCheckExistence, middleware.RequireAuth())
	api.POST("/drive/files/attached-notes", driveHandler.FilesAttachedNotes, middleware.RequireAuth())
	api.POST("/drive/files/attached-chat-messages", driveHandler.FilesAttachedChatMessages, middleware.RequireAuth())
	api.POST("/drive/files/upload-from-url", driveHandler.FilesUploadFromURL, middleware.RequireAuth())
	api.POST("/drive/files/move-bulk", driveHandler.FilesMoveBulk, middleware.RequireAuth())
	api.POST("/drive/stream", driveHandler.Stream, middleware.RequireAuth())
	// Phase 7.4: drive/* — 全て実データハンドラに移行済み

	// Static file serving for LocalStorage
	// MIME type はファイル内容の先頭から自動判定し、`http.ServeContent` で
	// Range / If-Modified-Since / Content-Length 対応の正しい応答を返す。
	//
	// 旧実装は echo の `c.Stream` + `io.MultiReader` で chunked transfer に
	// 倒していたが、Cloudflare Tunnel 経由のデプロイで bigger-than-buffer
	// (33KB 程度) なファイルが truncate される問題があった (#730)。明示
	// Content-Length + Cache-Control: no-transform でこれを回避する。
	s.echo.GET("/files/:accessKey", func(c echo.Context) error {
		key := c.Param("accessKey")
		body, err := driveStorage.Get(key)
		if err != nil {
			return c.NoContent(http.StatusNotFound)
		}
		defer body.Close()

		// LocalStorage は *os.File を返すので io.ReadSeeker として扱える。
		// 互換性のため type assertion で seekable かを確認し、非対応 (将来
		// S3 等の non-seekable storage を増やした場合) なら全 body を memory
		// に読み込む fallback にフォールバックする。
		// modtime も *os.File なら Stat() から取得して If-Modified-Since の
		// 304 経路を有効にする。それ以外は零値で http.ServeContent が
		// Last-Modified を出さない (Cache-Control: immutable で代替)。
		var modtime time.Time
		seeker, ok := body.(io.ReadSeeker)
		if ok {
			if f, isFile := body.(*os.File); isFile {
				if st, err := f.Stat(); err == nil {
					modtime = st.ModTime()
				}
			}
		} else {
			data, rerr := io.ReadAll(body)
			if rerr != nil {
				return c.NoContent(http.StatusInternalServerError)
			}
			seeker = bytes.NewReader(data)
		}

		// 先頭 512 バイト読んで MIME type を判定 → 元位置に戻す
		buf := make([]byte, 512)
		n, _ := seeker.Read(buf)
		contentType := http.DetectContentType(buf[:n])
		if _, err := seeker.Seek(0, io.SeekStart); err != nil {
			return c.NoContent(http.StatusInternalServerError)
		}

		// `Cache-Control: no-transform` で Cloudflare Polish 等 CDN 中間層
		// による画像再エンコードを抑止する (#730)。Polish は animated WebP
		// を別 size で出力するため UDS デプロイ環境で「絵文字がチカチカ
		// する + サイズが切れる」現象を起こす。`immutable` で長期 cache 化、
		// `no-transform` で binary 1:1 配信を契約する。
		c.Response().Header().Set("Cache-Control", "max-age=31536000, immutable, no-transform")
		c.Response().Header().Set(echo.HeaderContentType, contentType)
		// http.ServeContent が Content-Length / Range / If-Modified-Since を
		// 適切に処理する。modtime 取得できれば Last-Modified を発行して
		// 304 経路も活きる。
		http.ServeContent(c.Response(), c.Request(), key, modtime, seeker)
		return nil
	})

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
	federationHandler.SetResolver(federationResolver)
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
	api.POST("/channels/create", channelsHandler.Create, middleware.RequireAuth())
	api.POST("/channels/show", channelsHandler.Show)
	api.POST("/channels/update", channelsHandler.Update, middleware.RequireAuth())
	api.POST("/channels/follow", channelsHandler.Follow, middleware.RequireAuth())
	api.POST("/channels/unfollow", channelsHandler.Unfollow, middleware.RequireAuth())
	api.POST("/channels/followed", channelsHandler.Followed, middleware.RequireAuth())
	api.POST("/channels/owned", channelsHandler.Owned, middleware.RequireAuth())
	api.POST("/channels/featured", channelsHandler.Featured)
	api.POST("/channels/search", channelsHandler.Search)
	api.POST("/channels/timeline", channelsHandler.Timeline)
	channelsHandler.SetInstanceRepo(instanceRepo)
	channelsHandler.SetEmojiRepo(emojiRepo)
	channelsHandler.SetReactionReader(reactionCountWriter)
	channelsHandler.SetNoteFieldResolver(noteFieldResolver)

	// Antennas endpoints (Phase 4.3)
	antennasHandler := antennas.NewHandler(antennaService, noteRepo, idGen)
	antennasHandler.SetInstanceRepo(instanceRepo)
	antennasHandler.SetEmojiRepo(emojiRepo)
	antennasHandler.SetReactionReader(reactionCountWriter)
	antennasHandler.SetNoteFieldResolver(noteFieldResolver)
	api.POST("/antennas/create", antennasHandler.Create, middleware.RequireAuth())
	api.POST("/antennas/show", antennasHandler.Show, middleware.RequireAuth())
	api.POST("/antennas/update", antennasHandler.Update, middleware.RequireAuth())
	api.POST("/antennas/delete", antennasHandler.Delete, middleware.RequireAuth())
	api.POST("/antennas/list", antennasHandler.List, middleware.RequireAuth())
	api.POST("/antennas/notes", antennasHandler.Notes, middleware.RequireAuth())

	// Clips endpoints (Phase 4.4)
	clipsHandler := clips.NewHandler(clipService, idGen)
	clipsHandler.SetFavoriteRepo(clipFavoriteRepo)
	clipsHandler.SetInstanceRepo(instanceRepo)
	clipsHandler.SetEmojiRepo(emojiRepo)
	clipsHandler.SetReactionReader(reactionCountWriter)
	clipsHandler.SetNoteFieldResolver(noteFieldResolver)
	api.POST("/clips/create", clipsHandler.Create, middleware.RequireAuth())
	api.POST("/clips/show", clipsHandler.Show)
	api.POST("/clips/update", clipsHandler.Update, middleware.RequireAuth())
	api.POST("/clips/delete", clipsHandler.Delete, middleware.RequireAuth())
	api.POST("/clips/list", clipsHandler.List, middleware.RequireAuth())
	api.POST("/clips/add-note", clipsHandler.AddNote, middleware.RequireAuth())
	api.POST("/clips/remove-note", clipsHandler.RemoveNote, middleware.RequireAuth())
	api.POST("/clips/notes", clipsHandler.Notes)

	// Pages endpoints (Phase 4.5)
	pagesHandler := pages.NewHandler(pageService, idGen)
	api.POST("/pages/create", pagesHandler.Create, middleware.RequireAuth())
	api.POST("/pages/show", pagesHandler.Show)
	api.POST("/pages/update", pagesHandler.Update, middleware.RequireAuth())
	api.POST("/pages/delete", pagesHandler.Delete, middleware.RequireAuth())
	api.POST("/pages/featured", pagesHandler.Featured)
	api.POST("/pages/like", pagesHandler.Like, middleware.RequireAuth())
	api.POST("/pages/unlike", pagesHandler.Unlike, middleware.RequireAuth())
	api.POST("/i/pages", pagesHandler.My, middleware.RequireAuth())

	// Flash endpoints (Phase 4.5)
	flashHandler := apiflash.NewHandler(flashService, userRepo, idGen)
	api.POST("/flash/create", flashHandler.Create, middleware.RequireAuth())
	api.POST("/flash/show", flashHandler.Show)
	api.POST("/flash/update", flashHandler.Update, middleware.RequireAuth())
	api.POST("/flash/delete", flashHandler.Delete, middleware.RequireAuth())
	api.POST("/flash/featured", flashHandler.Featured)
	api.POST("/flash/search", flashHandler.Search)
	api.POST("/flash/like", flashHandler.Like, middleware.RequireAuth())
	api.POST("/flash/unlike", flashHandler.Unlike, middleware.RequireAuth())
	api.POST("/flash/my", flashHandler.My, middleware.RequireAuth())
	api.POST("/flash/my-likes", flashHandler.MyLikes, middleware.RequireAuth())
	api.POST("/i/flashs", flashHandler.My, middleware.RequireAuth())
	api.POST("/i/flashs/likes", flashHandler.MyLikes, middleware.RequireAuth())

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
	streamRegistry.Register("antenna", channels.NewAntenna)
	streamRegistry.Register("channel", channels.NewChannelTimeline)
	streamRegistry.Register("userList", channels.NewUserList)
	streamRegistry.Register("roleTimeline", channels.NewRoleTimeline)
	streamRegistry.Register("admin", channels.NewAdminFactory(roleService).New)
	// serverStats / queueStats は publisher を後段で構築するため、ここでは
	// 仮 register せず、publisher 生成後に登録する (1497 行付近)。

	// 3. Connection manager + 各 publisher の生成
	streamManager := stream.NewManager(streamRegistry, streamBus)
	// readNotificationメッセージをnotificationServiceに橋渡しする
	streamManager.SetNotificationReader(&notifReaderAdapter{svc: notificationService})
	notePublisher := stream.NewNotePublisher(streamPubSub, idGen)
	notePublisher.SetEmojiLookup(emojiRepo)
	notePublisher.SetInstanceLookup(instanceRepo)
	notePublisher.SetReactionReader(reactionCountWriter)
	notePublisher.SetFieldResolver(noteFieldResolver)
	notificationPublisher := stream.NewNotificationPublisher(streamPubSub)
	notificationPublisher.SetRepos(userRepo, noteRepo, idGen)
	notificationPublisher.SetInstanceLookup(instanceRepo)
	notificationPublisher.SetEmojiLookup(emojiRepo)
	drivePublisher := stream.NewDrivePublisher(streamPubSub)
	reversiPublisher := stream.NewReversiGamePublisher(streamPubSub)
	mainStreamPublisher := stream.NewMainStreamPublisher(streamPubSub)

	// server / queue stats publishers (#344)。起動時から tick を回して
	// `serverStats` / `queueStats` トピックへ定期 publish する。
	// ShutdownでStop()を呼ぶ (server.go 側でdefer)。
	// publisher が ring buffer を持つので、channel factory に StatsLogProvider
	// として渡して requestLog 応答に historical 値を返せるようにする (#571 item 2)。
	serverStatsPub := stream.NewServerStatsPublisher(streamPubSub, 0)
	serverStatsPub.Start()
	s.registerShutdownHook(serverStatsPub.Stop)
	streamRegistry.Register("serverStats", channels.NewServerStatsFactory(serverStatsPub))
	if s.queueInspector != nil {
		queueStatsPub := stream.NewQueueStatsPublisher(&queueStatsInspectorAdapter{inner: s.queueInspector}, streamPubSub, 0)
		queueStatsPub.Start()
		s.registerShutdownHook(queueStatsPub.Stop)
		streamRegistry.Register("queueStats", channels.NewQueueStatsFactory(queueStatsPub))
	} else {
		// queueInspector が無い起動 (テスト等) では fallback factory で空配列のみ返す
		streamRegistry.Register("queueStats", channels.NewQueueStats)
	}

	// 4. 既存サービスへ publisher を注入する。これらはいずれも nil 安全な
	//    setter で、未設定なら何もしない (テスト互換)。
	timelineFanoutHook.SetStreamingPublisher(notePublisher)
	notificationService.SetStreamingPublisher(notificationPublisher)
	notificationService.SetMainStreamPublisher(mainStreamPublisher)
	notificationService.SetPacker(notificationPublisher)
	driveService.SetStreamingPublisher(drivePublisher)
	driveService.SetMainStreamPublisher(mainStreamPublisher)
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
	federationProcessor.SetChatService(chatService)
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
	chartsHandler := apicharts.NewHandler(chartCharts, nil)
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
	api.POST("/following/create", followingHandler.Create, middleware.RequireAuth())
	api.POST("/following/delete", followingHandler.Delete, middleware.RequireAuth())
	api.POST("/following/requests/list", followingHandler.ListRequests, middleware.RequireAuth())
	api.POST("/following/requests/accept", followingHandler.AcceptRequest, middleware.RequireAuth())
	api.POST("/following/requests/reject", followingHandler.RejectRequest, middleware.RequireAuth())
	api.POST("/following/requests/cancel", followingHandler.CancelRequest, middleware.RequireAuth())
	// following 残り
	api.POST("/following/invalidate", followingHandler.Invalidate, middleware.RequireAuth())
	api.POST("/following/update", followingHandler.UpdateFollow, middleware.RequireAuth())
	api.POST("/following/update-all", followingHandler.UpdateFollowAll, middleware.RequireAuth())
	api.POST("/following/requests/sent", followingHandler.RequestsSent, middleware.RequireAuth())
	// channels 残り
	api.POST("/channels/favorite", channelsHandler.Favorite, middleware.RequireAuth())
	api.POST("/channels/unfavorite", channelsHandler.Unfavorite, middleware.RequireAuth())
	api.POST("/channels/mute/create", channelsHandler.MuteCreate, middleware.RequireAuth())
	api.POST("/channels/mute/delete", channelsHandler.MuteDelete, middleware.RequireAuth())
	api.POST("/channels/my-favorites", channelsHandler.MyFavorites, middleware.RequireAuth())
	api.POST("/channels/mute/list", channelsHandler.MuteList, middleware.RequireAuth())
	// clips 残り
	api.POST("/clips/favorite", clipsHandler.Favorite, middleware.RequireAuth())
	api.POST("/clips/unfavorite", clipsHandler.Unfavorite, middleware.RequireAuth())
	api.POST("/clips/my-favorites", clipsHandler.MyFavorites, middleware.RequireAuth())
	// federation の残ルートは上で登録済 (federationHandler 初期化直後)

	// Public roles (Phase 6)
	rolesHandler := apiroles.NewHandler(roleService, idGen)
	rolesHandler.SetNotesQuery(repository.NewRoleNotesQuery(s.db))
	rolesHandler.SetInstanceRepo(instanceRepo)
	rolesHandler.SetEmojiRepo(emojiRepo)
	rolesHandler.SetReactionReader(reactionCountWriter)
	rolesHandler.SetNoteFieldResolver(noteFieldResolver)
	api.POST("/roles/list", rolesHandler.List)
	api.POST("/roles/show", rolesHandler.Show)
	api.POST("/roles/users", rolesHandler.Users)
	api.POST("/roles/notes", rolesHandler.Notes)

	// User lists (Phase 6)
	userListHandler := apiuserlists.NewHandler(userListRepo, idGen)
	api.POST("/users/lists/list", userListHandler.List, middleware.RequireAuth())
	api.POST("/users/lists/create", userListHandler.Create, middleware.RequireAuth())
	api.POST("/users/lists/show", userListHandler.Show, middleware.RequireAuth())
	api.POST("/users/lists/push", userListHandler.Push, middleware.RequireAuth())
	api.POST("/users/lists/pull", userListHandler.Pull, middleware.RequireAuth())
	api.POST("/users/lists/delete", userListHandler.Delete, middleware.RequireAuth())

	// Announcements (Phase 6)
	announcementRepo := repository.NewAnnouncementRepository(s.db)
	// Phase 7-2 (#244): /api/i の hasUnreadAnnouncement / unreadAnnouncements 配線
	iHandler.SetAnnouncementRepo(announcementRepo)
	announcementHandler := apiannouncements.NewHandler(announcementRepo, idGen)
	announcementHandler.SetMainStreamPublisher(mainStreamPublisher)
	api.POST("/announcements", announcementHandler.List)
	api.POST("/i/read-announcement", announcementHandler.ReadAnnouncement, middleware.RequireAuth())

	// Admin endpoints (Phase 5)
	abuseReportRepo := repository.NewAbuseReportRepository(s.db)
	modLogRepo := repository.NewModerationLogRepository(s.db)
	recipientRepo := repository.NewAbuseReportNotificationRecipientRepository(s.db)
	adminHandler := apiadmin.NewHandler(signupService, roleService, metaRepo, userRepo, idGen)
	adminHandler.SetInstanceRepo(instanceRepo)
	adminHandler.SetAbuseRepo(abuseReportRepo)
	adminHandler.SetAbuseForwarder(coreabuse.NewForwarder(abuseReportRepo, sysAcctSvc, apRenderer, deliverService))
	adminHandler.SetDeleteAccountEnqueuer(s.queueClient)
	adminHandler.SetPasswordResetRepo(resetReqRepo)
	adminHandler.SetServerURL(s.config.URL)
	adminHandler.SetConfigSetupPassword(s.config.SetupPassword)
	adminHandler.SetSMTPProxyURL(s.config.ProxySmtp)
	if smtpMetaAdmin, err := metaRepo.Fetch(); err == nil && smtpMetaAdmin.EnableEmail && smtpMetaAdmin.Email != nil && smtpMetaAdmin.SmtpHost != nil {
		fromAddr := *smtpMetaAdmin.Email
		host := *smtpMetaAdmin.SmtpHost
		port := 587
		if smtpMetaAdmin.SmtpPort != nil {
			port = *smtpMetaAdmin.SmtpPort
		}
		smtpUser, smtpPass := smtpMetaAdmin.SmtpUser, smtpMetaAdmin.SmtpPass
		adminHandler.SetEmailSender(func(to, subject, body string) {
			miscsmtp.SendWithOptions(host, port, smtpUser, smtpPass, fromAddr, to, subject, body, miscsmtp.Options{ProxyURL: s.config.ProxySmtp})
		})
	}
	modLogService := coremodlog.New(modLogRepo, idGen)
	adminHandler.SetModLogService(modLogService)
	// announcements (別パッケージの Handler) も AdminCreate/Update/Delete で
	// modlog を書くため同じ service instance を共有する。
	announcementHandler.SetModLogService(modLogService)
	announcementHandler.SetUserRepo(userRepo)
	adminHandler.SetEmojiRepo(emojiRepo)
	adminHandler.SetDriveFileRepo(driveFileRepo)
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
	api.POST("/admin/show-user", adminHandler.ShowUser, middleware.RequireModerator(roleService))
	api.POST("/admin/show-users", adminHandler.ShowUsers, middleware.RequireModerator(roleService))
	api.POST("/admin/suspend-user", adminHandler.SuspendUser, middleware.RequireModerator(roleService))
	api.POST("/admin/unsuspend-user", adminHandler.UnsuspendUser, middleware.RequireModerator(roleService))
	api.POST("/admin/meta", adminHandler.AdminMeta, middleware.RequireAdmin(roleService))
	api.POST("/admin/update-meta", adminHandler.UpdateMeta, middleware.RequireAdmin(roleService))
	api.POST("/admin/roles/create", adminHandler.RolesCreate, middleware.RequireAdmin(roleService))
	api.POST("/admin/roles/show", adminHandler.RolesShow, middleware.RequireAdmin(roleService))
	api.POST("/admin/roles/list", adminHandler.RolesList, middleware.RequireAdmin(roleService))
	api.POST("/admin/roles/update", adminHandler.RolesUpdate, middleware.RequireAdmin(roleService))
	api.POST("/admin/roles/delete", adminHandler.RolesDelete, middleware.RequireAdmin(roleService))
	api.POST("/admin/roles/assign", adminHandler.RolesAssign, middleware.RequireModerator(roleService))
	api.POST("/admin/roles/unassign", adminHandler.RolesUnassign, middleware.RequireModerator(roleService))
	api.POST("/admin/roles/users", adminHandler.RolesUsers, middleware.RequireModerator(roleService))
	api.POST("/admin/roles/update-default-policies", adminHandler.RolesUpdateDefaultPolicies, middleware.RequireAdmin(roleService))
	api.POST("/admin/abuse-user-reports", adminHandler.AbuseReports, middleware.RequireModerator(roleService))
	api.POST("/admin/resolve-abuse-user-report", adminHandler.ResolveAbuseReport, middleware.RequireModerator(roleService))
	api.POST("/admin/show-moderation-logs", adminHandler.ShowModerationLogs, middleware.RequireModerator(roleService))
	api.POST("/admin/emoji/add", adminHandler.EmojiAdd, middleware.RequireAdmin(roleService))
	api.POST("/admin/emoji/update", adminHandler.EmojiUpdate, middleware.RequireAdmin(roleService))
	api.POST("/admin/emoji/delete", adminHandler.EmojiDelete, middleware.RequireAdmin(roleService))
	api.POST("/admin/emoji/list", adminHandler.EmojiList, middleware.RequireAdmin(roleService))
	api.POST("/admin/announcements/create", announcementHandler.AdminCreate, middleware.RequireAdmin(roleService))
	api.POST("/admin/announcements/update", announcementHandler.AdminUpdate, middleware.RequireAdmin(roleService))
	api.POST("/admin/announcements/delete", announcementHandler.AdminDelete, middleware.RequireAdmin(roleService))
	api.POST("/admin/announcements/list", announcementHandler.AdminList, middleware.RequireAdmin(roleService))

	// Phase 7.5: admin/* 残りエンドポイント (ハンドラメソッド化)
	api.POST("/admin/delete-account", adminHandler.DeleteAccount, middleware.RequireModerator(roleService))
	api.POST("/admin/delete-all-files-of-a-user", adminHandler.DeleteAllFilesOfUser, middleware.RequireModerator(roleService))
	api.POST("/admin/reset-password", adminHandler.ResetPassword, middleware.RequireModerator(roleService))
	api.POST("/admin/send-email", adminHandler.SendEmail, middleware.RequireModerator(roleService))
	api.POST("/admin/unset-user-avatar", adminHandler.UnsetUserAvatar, middleware.RequireModerator(roleService))
	api.POST("/admin/unset-user-banner", adminHandler.UnsetUserBanner, middleware.RequireModerator(roleService))
	api.POST("/admin/update-user-note", adminHandler.UpdateUserNote, middleware.RequireModerator(roleService))
	api.POST("/admin/update-proxy-account", adminHandler.UpdateProxyAccount, middleware.RequireModerator(roleService))
	api.POST("/admin/forward-abuse-user-report", adminHandler.ForwardAbuseUserReport, middleware.RequireModerator(roleService))
	api.POST("/admin/update-abuse-user-report", adminHandler.UpdateAbuseUserReport, middleware.RequireModerator(roleService))
	api.POST("/admin/accounts/delete", adminHandler.AccountsDelete, middleware.RequireModerator(roleService))
	api.POST("/admin/accounts/find-by-email", adminHandler.AccountsFindByEmail, middleware.RequireModerator(roleService))
	api.POST("/admin/get-user-ips", adminHandler.GetUserIPs, middleware.RequireModerator(roleService))
	api.POST("/admin/get-index-stats", adminHandler.GetIndexStats, middleware.RequireAdmin(roleService))
	api.POST("/admin/get-table-stats", adminHandler.GetTableStats, middleware.RequireAdmin(roleService))
	api.POST("/admin/server-info", adminHandler.ServerInfo, middleware.RequireAdmin(roleService))
	api.POST("/admin/captcha/current", adminHandler.CaptchaCurrent, middleware.RequireAdmin(roleService))
	api.POST("/admin/captcha/save", adminHandler.CaptchaSave, middleware.RequireModerator(roleService))
	api.POST("/admin/ad/create", adminHandler.AdCreate, middleware.RequireModerator(roleService))
	api.POST("/admin/ad/delete", adminHandler.AdDelete, middleware.RequireModerator(roleService))
	api.POST("/admin/ad/list", adminHandler.AdList, middleware.RequireModerator(roleService))
	api.POST("/admin/ad/update", adminHandler.AdUpdate, middleware.RequireModerator(roleService))
	api.POST("/admin/avatar-decorations/create", adminHandler.AvatarDecorationsCreate, middleware.RequireModerator(roleService))
	api.POST("/admin/avatar-decorations/delete", adminHandler.AvatarDecorationsDelete, middleware.RequireModerator(roleService))
	api.POST("/admin/avatar-decorations/list", adminHandler.AvatarDecorationsList, middleware.RequireModerator(roleService))
	api.POST("/admin/avatar-decorations/update", adminHandler.AvatarDecorationsUpdate, middleware.RequireModerator(roleService))
	api.POST("/admin/drive/clean-remote-files", adminHandler.DriveCleanRemoteFiles, middleware.RequireModerator(roleService))
	api.POST("/admin/drive/cleanup", adminHandler.DriveCleanup, middleware.RequireModerator(roleService))
	api.POST("/admin/drive/files", adminHandler.DriveFiles, middleware.RequireModerator(roleService))
	api.POST("/admin/drive/show-file", adminHandler.DriveShowFile, middleware.RequireModerator(roleService))
	api.POST("/admin/emoji/add-aliases-bulk", adminHandler.EmojiAddAliasesBulk, middleware.RequireModerator(roleService))
	api.POST("/admin/emoji/copy", adminHandler.EmojiCopy, middleware.RequireModerator(roleService))
	api.POST("/admin/emoji/delete-bulk", adminHandler.EmojiDeleteBulk, middleware.RequireModerator(roleService))
	api.POST("/admin/emoji/import-zip", adminHandler.EmojiImportZip, middleware.RequireModerator(roleService))
	api.POST("/admin/emoji/list-remote", adminHandler.EmojiListRemote, middleware.RequireModerator(roleService))
	api.POST("/admin/emoji/remove-aliases-bulk", adminHandler.EmojiRemoveAliasesBulk, middleware.RequireModerator(roleService))
	api.POST("/admin/emoji/set-aliases-bulk", adminHandler.EmojiSetAliasesBulk, middleware.RequireModerator(roleService))
	api.POST("/admin/emoji/set-category-bulk", adminHandler.EmojiSetCategoryBulk, middleware.RequireModerator(roleService))
	api.POST("/admin/emoji/set-license-bulk", adminHandler.EmojiSetLicenseBulk, middleware.RequireModerator(roleService))
	api.POST("/admin/federation/delete-all-files", adminHandler.FederationDeleteAllFiles, middleware.RequireModerator(roleService))
	api.POST("/admin/federation/refresh-remote-instance-metadata", adminHandler.FederationRefreshRemoteInstanceMetadata, middleware.RequireModerator(roleService))
	api.POST("/admin/federation/remove-all-following", adminHandler.FederationRemoveAllFollowing, middleware.RequireModerator(roleService))
	api.POST("/admin/federation/update-instance", adminHandler.FederationUpdateInstance, middleware.RequireModerator(roleService))
	api.POST("/admin/invite/create", adminHandler.InviteCreate, middleware.RequireModerator(roleService))
	api.POST("/admin/invite/list", adminHandler.InviteList, middleware.RequireModerator(roleService))
	api.POST("/admin/promo/create", adminHandler.PromoCreate, middleware.RequireModerator(roleService))
	api.POST("/admin/relays/add", adminHandler.RelaysAdd, middleware.RequireModerator(roleService))
	api.POST("/admin/relays/list", adminHandler.RelaysList, middleware.RequireModerator(roleService))
	api.POST("/admin/relays/remove", adminHandler.RelaysRemove, middleware.RequireModerator(roleService))
	api.POST("/admin/system-webhook/create", adminHandler.SystemWebhookCreate, middleware.RequireModerator(roleService))
	api.POST("/admin/system-webhook/delete", adminHandler.SystemWebhookDelete, middleware.RequireModerator(roleService))
	api.POST("/admin/system-webhook/list", adminHandler.SystemWebhookList, middleware.RequireModerator(roleService))
	api.POST("/admin/system-webhook/show", adminHandler.SystemWebhookShow, middleware.RequireModerator(roleService))
	api.POST("/admin/system-webhook/test", adminHandler.SystemWebhookTest, middleware.RequireModerator(roleService))
	api.POST("/admin/system-webhook/update", adminHandler.SystemWebhookUpdate, middleware.RequireModerator(roleService))
	api.POST("/admin/abuse-report/notification-recipient/create", adminHandler.AbuseReportNotificationRecipientCreate, middleware.RequireModerator(roleService))
	api.POST("/admin/abuse-report/notification-recipient/delete", adminHandler.AbuseReportNotificationRecipientDelete, middleware.RequireModerator(roleService))
	api.POST("/admin/abuse-report/notification-recipient/list", adminHandler.AbuseReportNotificationRecipientList, middleware.RequireModerator(roleService))
	api.POST("/admin/abuse-report/notification-recipient/show", adminHandler.AbuseReportNotificationRecipientShow, middleware.RequireModerator(roleService))
	api.POST("/admin/abuse-report/notification-recipient/update", adminHandler.AbuseReportNotificationRecipientUpdate, middleware.RequireModerator(roleService))
	api.POST("/admin/queue/clear", adminHandler.QueueClear, middleware.RequireAdmin(roleService))
	api.POST("/admin/queue/deliver-delayed", adminHandler.QueueDeliverDelayed, middleware.RequireModerator(roleService))
	api.POST("/admin/queue/inbox-delayed", adminHandler.QueueInboxDelayed, middleware.RequireModerator(roleService))
	api.POST("/admin/queue/jobs", adminHandler.QueueJobs, middleware.RequireModerator(roleService))
	api.POST("/admin/queue/promote-jobs", adminHandler.QueuePromoteJobs, middleware.RequireAdmin(roleService))
	api.POST("/admin/queue/queue-stats", adminHandler.QueueQueueStats, middleware.RequireAdmin(roleService))
	api.POST("/admin/queue/queues", adminHandler.QueueQueues, middleware.RequireAdmin(roleService))
	api.POST("/admin/queue/remove-job", adminHandler.QueueRemoveJob, middleware.RequireAdmin(roleService))
	api.POST("/admin/queue/retry-job", adminHandler.QueueRetryJob, middleware.RequireAdmin(roleService))
	api.POST("/admin/queue/show-job", adminHandler.QueueShowJob, middleware.RequireAdmin(roleService))
	api.POST("/admin/queue/show-job-logs", adminHandler.QueueShowJobLogs, middleware.RequireAdmin(roleService))
	api.POST("/admin/queue/stats", adminHandler.QueueStats, middleware.RequireAdmin(roleService))

	// --- Phase 7.6b: chat/*, auth/*, ap/*, sw/*, reversi/*, bubble-game/*, misc ---

	// announcements/show — 個別アナウンスメント取得
	api.POST("/announcements/show", announcementHandler.Show)

	// auth/* — MiAuth/OAuth セッション
	authSessionRepo := repository.NewAuthSessionRepository(s.db)
	authHandler := apiauth.NewHandler(authSessionRepo, s.config, idGen)
	api.POST("/auth/session/generate", authHandler.SessionGenerate)
	api.POST("/auth/session/show", authHandler.SessionShow)
	api.POST("/auth/session/userkey", authHandler.SessionUserkey)
	api.POST("/auth/accept", authHandler.Accept, middleware.RequireAuth())

	// miauth/gen-token — アクセストークン直接生成
	api.POST("/miauth/gen-token", authHandler.GenToken, middleware.RequireAuth())

	// app/* — アプリ管理API
	appHandler := apiapp.NewHandler(authSessionRepo, idGen)
	api.POST("/app/create", appHandler.Create)
	api.POST("/app/show", appHandler.Show)
	api.POST("/my/apps", appHandler.MyApps, middleware.RequireAuth())

	// ap/* — ActivityPub API lookup (実データ: ローカルオブジェクト解決)
	// /api/ap/notes は upstream Misskey にも存在しない vestigial route だった
	// ため #587 で削除済 (旧実装は常に空配列返却の stub)。
	api.POST("/ap/get", apHandler.APIGet, middleware.RequireAdmin(roleService))
	api.POST("/ap/show", apHandler.APIShow, middleware.RequireAuth())

	// sw/* — Service Worker push notifications (実データ)
	swHandler := apisw.NewHandler(swSubRepo, metaRepo, idGen)
	api.POST("/sw/register", swHandler.Register, middleware.RequireAuth())
	api.POST("/sw/show-registration", swHandler.ShowRegistration, middleware.RequireAuth())
	api.POST("/sw/update-registration", swHandler.UpdateRegistration, middleware.RequireAuth())
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
	api.POST("/reversi/invitations", reversiHandler.Invitations, middleware.RequireAuth())
	api.POST("/reversi/show-game", reversiHandler.ShowGame)
	api.POST("/reversi/match", reversiHandler.Match, middleware.RequireAuth())
	api.POST("/reversi/cancel-match", reversiHandler.CancelMatch, middleware.RequireAuth())
	api.POST("/reversi/surrender", reversiHandler.Surrender, middleware.RequireAuth())
	api.POST("/reversi/verify", reversiHandler.Verify)

	// bubble-game/* — バブルゲーム (実データ)
	bubbleGameRepo := repository.NewBubbleGameRepository(s.db)
	bubbleGameHandler := apibubblegame.NewHandler(bubbleGameRepo, idGen)
	api.POST("/bubble-game/register", bubbleGameHandler.Register, middleware.RequireAuth())
	api.POST("/bubble-game/ranking", bubbleGameHandler.Ranking)

	// chat/* — Misskey v2026 チャット機能 (実データ)
	chatHandler := apichat.NewHandler(chatRepo, idGen)
	chatHandler.SetService(chatService)
	api.POST("/chat/rooms/create", chatHandler.RoomsCreate, middleware.RequireAuth())
	api.POST("/chat/rooms/show", chatHandler.RoomsShow, middleware.RequireAuth())
	api.POST("/chat/rooms/update", chatHandler.RoomsUpdate, middleware.RequireAuth())
	api.POST("/chat/rooms/delete", chatHandler.RoomsDelete, middleware.RequireAuth())
	api.POST("/chat/rooms/owned", chatHandler.RoomsOwned, middleware.RequireAuth())
	api.POST("/chat/rooms/joined", chatHandler.RoomsJoined, middleware.RequireAuth())
	api.POST("/chat/rooms/leave", chatHandler.RoomsLeave, middleware.RequireAuth())
	api.POST("/chat/rooms/mute", chatHandler.RoomsMute, middleware.RequireAuth())
	api.POST("/chat/rooms/unmute", chatHandler.RoomsUnmute, middleware.RequireAuth())
	api.POST("/chat/rooms/transfer-ownership", chatHandler.RoomsTransferOwnership, middleware.RequireAuth())
	api.POST("/chat/rooms/join", chatHandler.RoomsJoin, middleware.RequireAuth())
	api.POST("/chat/rooms/joining", chatHandler.RoomsJoining, middleware.RequireAuth())
	api.POST("/chat/rooms/members", chatHandler.RoomsMembers, middleware.RequireAuth())
	api.POST("/chat/rooms/invitations/create", chatHandler.InvitationsCreate, middleware.RequireAuth())
	api.POST("/chat/rooms/invitations/delete", chatHandler.InvitationsDelete, middleware.RequireAuth())
	api.POST("/chat/rooms/invitations/accept", chatHandler.InvitationsAccept, middleware.RequireAuth())
	api.POST("/chat/rooms/invitations/reject", chatHandler.InvitationsReject, middleware.RequireAuth())
	api.POST("/chat/rooms/invitations/ignore", chatHandler.InvitationsIgnore, middleware.RequireAuth())
	api.POST("/chat/rooms/invitations/inbox", chatHandler.InvitationsInbox, middleware.RequireAuth())
	api.POST("/chat/rooms/invitations/outbox", chatHandler.InvitationsOutbox, middleware.RequireAuth())
	api.POST("/chat/rooms/members/ban", chatHandler.MembersBan, middleware.RequireAuth())
	api.POST("/chat/rooms/members/update-membership", chatHandler.MembersUpdateMembership, middleware.RequireAuth())
	api.POST("/chat/messages", chatHandler.Messages, middleware.RequireAuth())
	api.POST("/chat/messages/create", chatHandler.MessagesCreate, middleware.RequireAuth())
	api.POST("/chat/messages/create-to-user", chatHandler.MessagesCreateToUser, middleware.RequireAuth())
	api.POST("/chat/messages/create-to-room", chatHandler.MessagesCreateToRoom, middleware.RequireAuth())
	api.POST("/chat/messages/show", chatHandler.MessagesShow, middleware.RequireAuth())
	api.POST("/chat/messages/update", chatHandler.MessagesUpdate, middleware.RequireAuth())
	api.POST("/chat/messages/delete", chatHandler.MessagesDelete, middleware.RequireAuth())
	api.POST("/chat/messages/read", chatHandler.MessagesRead, middleware.RequireAuth())
	api.POST("/chat/messages/search", chatHandler.MessagesSearch, middleware.RequireAuth())
	api.POST("/chat/messages/user-timeline", chatHandler.UserTimeline, middleware.RequireAuth())
	api.POST("/chat/messages/room-timeline", chatHandler.RoomTimeline, middleware.RequireAuth())
	api.POST("/chat/messages/react", chatHandler.ReactionsCreate, middleware.RequireAuth())
	api.POST("/chat/messages/unreact", chatHandler.ReactionsDelete, middleware.RequireAuth())
	api.POST("/chat/messages/reactions/create", chatHandler.ReactionsCreate, middleware.RequireAuth())
	api.POST("/chat/messages/reactions/delete", chatHandler.ReactionsDelete, middleware.RequireAuth())
	api.POST("/chat/history", chatHandler.History, middleware.RequireAuth())
	api.POST("/chat/read-all", chatHandler.ReadAll, middleware.RequireAuth())
	api.POST("/chat/unread-count", chatHandler.UnreadCount, middleware.RequireAuth())

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
		if m, err := metaRepo.Fetch(); err == nil && m.EnableServerMachineStats {
			return c.JSON(http.StatusOK, serverstats.Collect())
		}
		return c.JSON(http.StatusOK, serverstats.Empty())
	}
	api.POST("/server-info", serverInfoHandler)
	api.GET("/server-info", serverInfoHandler)

	// endpoints — 登録済みAPIエンドポイント一覧
	api.POST("/endpoints", func(c echo.Context) error {
		routes := s.echo.Routes()
		names := make([]string, 0, len(routes))
		seen := make(map[string]bool)
		for _, r := range routes {
			if r.Method != http.MethodPost {
				continue
			}
			name := strings.TrimPrefix(r.Path, "/api/")
			if name == r.Path || name == "" || name == "*" {
				continue
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
		return c.JSON(http.StatusOK, names)
	})

	// endpoint — エンドポイント情報 (簡易版: パラメータ情報なし)
	api.POST("/endpoint", func(c echo.Context) error {
		var req struct {
			Endpoint string `json:"endpoint"`
		}
		if err := c.Bind(&req); err != nil || req.Endpoint == "" {
			return c.JSON(http.StatusBadRequest, map[string]any{
				"error": map[string]any{
					"message": "Invalid param.",
					"code":    "INVALID_PARAM",
					"id":      "3d81ceae-475f-4600-b2a8-2bc116157532",
				},
			})
		}
		return c.JSON(http.StatusOK, map[string]any{"params": map[string]any{}})
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
			})
		}
		return c.JSON(http.StatusOK, out)
	})

	// email-address/available — メールアドレスの利用可否チェック (認証必須)
	api.POST("/email-address/available", func(c echo.Context) error {
		var req struct {
			EmailAddress string `json:"emailAddress"`
		}
		if err := c.Bind(&req); err != nil || req.EmailAddress == "" {
			return c.JSON(http.StatusBadRequest, map[string]any{
				"error": map[string]any{
					"message": "Invalid param.",
					"code":    "INVALID_PARAM",
					"id":      "3d81ceae-475f-4600-b2a8-2bc116157532",
				},
			})
		}
		var count int64
		s.db.Model(&model.UserProfile{}).Where(`"email" = ?`, req.EmailAddress).Count(&count)
		available := count == 0
		var reason *string
		if !available {
			r := "unavailable"
			reason = &r
		}
		return c.JSON(http.StatusOK, map[string]any{
			"available": available,
			"reason":    reason,
		})
	}, middleware.RequireAuth())

	// promo/read — プロモノートの既読マーク (認証必須)
	api.POST("/promo/read", func(c echo.Context) error {
		user := middleware.GetUser(c)
		var req struct {
			NoteID string `json:"noteId"`
		}
		if err := c.Bind(&req); err != nil || req.NoteID == "" {
			return c.JSON(http.StatusBadRequest, map[string]any{
				"error": map[string]any{
					"message": "Invalid param.",
					"code":    "INVALID_PARAM",
					"id":      "3d81ceae-475f-4600-b2a8-2bc116157532",
				},
			})
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
	}, middleware.RequireAuth())

	// invite/create — 招待コード作成 (認証必須)
	api.POST("/invite/create", func(c echo.Context) error {
		user := middleware.GetUser(c)
		ticket := &model.RegistrationTicket{
			ID:          idGen.Generate(time.Now()),
			Code:        generateInviteCode(),
			CreatedByID: &user.ID,
		}
		if err := s.db.Create(ticket).Error; err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]any{
				"error": map[string]any{
					"message": "Internal error.",
					"code":    "INTERNAL_ERROR",
					"id":      "5d37dbcb-891e-41ca-a3d6-e690c97775ac",
				},
			})
		}
		createdBy := entity.PackUserLite(user)
		resp := map[string]any{
			"id":        ticket.ID,
			"code":      ticket.Code,
			"expiresAt": ticket.ExpiresAt,
			"createdBy": createdBy,
			"usedBy":    nil,
			"usedAt":    nil,
			"used":      false,
		}
		if t, err := idGen.ParseTime(ticket.ID); err == nil {
			resp["createdAt"] = t.UTC().Format("2006-01-02T15:04:05.000Z")
		}
		return c.JSON(http.StatusOK, resp)
	}, middleware.RequireAuth())

	// invite/list — 招待コード一覧 (認証必須)
	api.POST("/invite/list", func(c echo.Context) error {
		user := middleware.GetUser(c)
		var req struct {
			Limit   int    `json:"limit"`
			SinceID string `json:"sinceId"`
			UntilID string `json:"untilId"`
		}
		_ = c.Bind(&req)
		if req.Limit <= 0 {
			req.Limit = 30
		}
		if req.Limit > 100 {
			req.Limit = 100
		}
		q := s.db.Model(&model.RegistrationTicket{}).
			Where(`"createdById" = ?`, user.ID).
			Order("id DESC").
			Limit(req.Limit)
		if req.SinceID != "" {
			q = q.Where("id > ?", req.SinceID)
		}
		if req.UntilID != "" {
			q = q.Where("id < ?", req.UntilID)
		}
		var tickets []*model.RegistrationTicket
		if err := q.Find(&tickets).Error; err != nil {
			return c.JSON(http.StatusOK, []any{})
		}
		out := make([]map[string]any, 0, len(tickets))
		for _, t := range tickets {
			entry := map[string]any{
				"id":        t.ID,
				"code":      t.Code,
				"expiresAt": t.ExpiresAt,
				"createdBy": nil,
				"usedBy":    nil,
				"usedAt":    t.UsedAt,
				"used":      t.UsedByID != nil,
			}
			if ts, err := idGen.ParseTime(t.ID); err == nil {
				entry["createdAt"] = ts.UTC().Format("2006-01-02T15:04:05.000Z")
			}
			out = append(out, entry)
		}
		return c.JSON(http.StatusOK, out)
	}, middleware.RequireAuth())

	// invite/delete — 自分が作成した招待コード削除
	api.POST("/invite/delete", func(c echo.Context) error {
		user := middleware.GetUser(c)
		var req struct {
			InviteID string `json:"inviteId"`
		}
		if err := c.Bind(&req); err != nil || req.InviteID == "" {
			return c.JSON(http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "inviteId is required.", "code": "INVALID_PARAM", "id": "3d81ceae-475f-4600-b2a8-2bc116157532"}})
		}
		var ticket model.RegistrationTicket
		if err := s.db.Where("id = ?", req.InviteID).First(&ticket).Error; err != nil {
			return c.NoContent(http.StatusNoContent)
		}
		if ticket.CreatedByID == nil || *ticket.CreatedByID != user.ID {
			return c.JSON(http.StatusForbidden, map[string]any{"error": map[string]any{"message": "Access denied.", "code": "ACCESS_DENIED", "id": "1fb7cb09-d46a-4fff-b8df-057708cce513"}})
		}
		if err := s.db.Where("id = ?", req.InviteID).Delete(&model.RegistrationTicket{}).Error; err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]any{"error": map[string]any{"message": "Internal error.", "code": "INTERNAL_ERROR", "id": "5d37dbcb-891e-41ca-a3d6-e690c97775ac"}})
		}
		return c.NoContent(http.StatusNoContent)
	}, middleware.RequireAuth())

	// invite/limit — 残り招待枠を返す (policies から取得、簡易実装)
	api.POST("/invite/limit", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{
			"remaining": 0,
		})
	}, middleware.RequireAuth())

	// notes (plain) — bulk note lookup by noteIds
	api.POST("/notes", notesHandler.BulkShow)

	// export-custom-emojis — zip export (complex, stub)
	api.POST("/export-custom-emojis", func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	}, middleware.RequireAuth())

	// fetch-external-resources — URL proxy (stub)
	api.POST("/fetch-external-resources", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"type": "unknown", "data": map[string]any{}})
	})

	// fetch-rss — RSS / Atom feed プロキシ。frontend RSS / RSSTicker ウィジェット
	// が GET で叩く実装になっているため Misskey TS と同じく allowGet 相当で
	// GET / POST の両方を受け付ける。
	fetchRSSHandler := apifetchrss.New(s.outboundClient(apifetchrss.FetchTimeout), s.config.UserAgent)
	api.GET("/fetch-rss", fetchRSSHandler.Fetch)
	api.POST("/fetch-rss", fetchRSSHandler.Fetch)

	// page-push — page scriptが任意のeventをpage所有者のmainに emit する。
	api.POST("/page-push", pagesHandler.PagePush, middleware.RequireAuth())

	// v2/admin/emoji/list — v2はページネーション情報付きオブジェクトを返す専用ハンドラ
	api.POST("/v2/admin/emoji/list", adminHandler.EmojiListV2, middleware.RequireAdmin(roleService))

	// --- その他の残りエンドポイント ---
	// test — フロントエンドのテスト用
	api.POST("/test", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{})
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

	// twemoji SVG配信
	twemojiDir := frontendutil.TwemojiDir()
	if _, err := os.Stat(twemojiDir); err == nil {
		s.echo.Static("/twemoji", twemojiDir)
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

// generateInviteCode creates a random invite code string.
func generateInviteCode() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	if _, err := io.ReadFull(cryptorand.Reader, b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	for i := range b {
		b[i] = chars[b[i]%byte(len(chars))]
	}
	return string(b)
}

// notifReaderAdapter bridges stream.NotificationReader to
// core/notification.Service.MarkAllAsRead.
type notifReaderAdapter struct {
	svc *corenotification.Service
}

func (a *notifReaderAdapter) ReadAll(userID string) error {
	return a.svc.MarkAllAsRead(context.Background(), userID)
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
