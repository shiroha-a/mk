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
	coreinstance "github.com/shiroha-a/mk/internal/core/instance"
	coremediaproxy "github.com/shiroha-a/mk/internal/core/mediaproxy"
	coremove "github.com/shiroha-a/mk/internal/core/move"
	coremuting "github.com/shiroha-a/mk/internal/core/muting"
	corenote "github.com/shiroha-a/mk/internal/core/note"
	corenotification "github.com/shiroha-a/mk/internal/core/notification"
	corepage "github.com/shiroha-a/mk/internal/core/page"
	corepoll "github.com/shiroha-a/mk/internal/core/poll"
	corereaction "github.com/shiroha-a/mk/internal/core/reaction"
	corerelay "github.com/shiroha-a/mk/internal/core/relay"
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
	"gorm.io/gorm"
	"log/slog"
)

func (s *Server) setupRoutes() {
	idGen, err := id.NewGenerator(s.config.ID)
	if err != nil {
		idGen, _ = id.NewGenerator("aidx")
	}

	// Repositories
	userRepo := repository.NewUserRepository(s.db)
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
	emojiRepo := repository.NewEmojiRepository(s.db)
	blockingRepo := repository.NewBlockingRepository(s.db)
	mutingRepo := repository.NewMutingRepository(s.db)
	renoteMutingRepo := repository.NewRenoteMutingRepository(s.db)
	pollVoteRepo := repository.NewPollVoteRepository(s.db)
	driveFileRepo := repository.NewDriveFileRepository(s.db)
	driveFolderRepo := repository.NewDriveFolderRepository(s.db)
	keypairRepo := repository.NewUserKeypairRepository(s.db)
	instanceRepo := repository.NewInstanceRepository(s.db)
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
	followingService := corefollowing.NewService(userRepo, followingRepo, followRequestRepo, idGen)

	// Timeline services (Redis-backed fanout)
	fanoutTimelineService := coretimeline.NewFanoutTimelineService(s.redis.Timelines, idGen)
	timelineService := coretimeline.NewService(fanoutTimelineService, noteRepo, followingRepo)
	timelineFanoutHook := coretimeline.NewFanoutHook(fanoutTimelineService, followingRepo)
	// Phase DB-compat (#51): meta から timeline cache cap を動的に読む。
	// 4 つのカラム (perLocal / perRemote / perHome / perList) が反映される。
	timelineFanoutHook.SetCacheLimitsProvider(coretimeline.NewMetaRepoCacheLimits(metaRepo))
	noteCreateService.SetFanoutHook(timelineFanoutHook)

	// Channels (Phase 4.2)
	channelService := corechannel.NewService(channelRepo, channelFollowingRepo, noteRepo, idGen)
	noteCreateService.SetChannelHook(corechannel.NewNoteCreateHook(channelService))

	// Antennas (Phase 4.3)
	antennaService := coreantenna.NewService(antennaRepo, userRepo, s.redis.Default, idGen)
	antennaService.SetFollowingRepo(followingRepo)
	antennaService.SetUserListRepo(userListRepo)
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
	notificationService := corenotification.NewService(s.redis.Default, idGen)
	notificationHook := corenotification.NewHook(notificationService, userRepo)
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
	webPushProcessor := processors.NewWebPushProcessor(
		webPushCache, swSubRepo, metaRepo, nil, s.config.URL,
	)
	s.queueServer.Handle(queue.TaskTypeWebPush, webPushProcessor.Handle)

	// Webhook (Phase 9.5): ユーザー/システムwebhookの配信を非同期処理する。
	webhookService := corewebhook.NewService(s.queueClient, webhookRepo, systemWebhookRepo, s.config.URL)
	noteCreateService.SetWebhookHook(corewebhook.NewNoteCreateHook(webhookService, idGen))
	reactionService.SetWebhookHook(corewebhook.NewReactionCreateHook(webhookService, idGen))
	followingService.SetWebhookHook(corewebhook.NewFollowingHook(webhookService))
	signupService.SetWebhookHook(corewebhook.NewSignupHook(webhookService))
	webhookProcessor := processors.NewWebhookProcessor(webhookRepo, systemWebhookRepo, nil, s.config.Host)
	s.queueServer.Handle(queue.TaskTypeUserWebhook, webhookProcessor.HandleUser)
	s.queueServer.Handle(queue.TaskTypeSystemWebhook, webhookProcessor.HandleSystem)

	// Blocking & Muting
	blockingService := coreblocking.NewService(userRepo, blockingRepo, followingRepo, idGen)
	mutingService := coremuting.NewService(userRepo, mutingRepo, idGen)
	renoteMutingService := coremuting.NewRenoteService(userRepo, renoteMutingRepo, idGen)
	followingService.SetBlockingChecker(blockingService)
	reactionService.SetBlockingChecker(blockingService)
	notificationHook.SetMuteChecker(mutingService)

	// Polls
	pollService := corepoll.NewService(noteRepo, pollRepo, pollVoteRepo, followingRepo, idGen)
	pollService.SetNotificationHook(notificationHook)

	// Drive storage (Phase 4.9: Meta に基づいて Local / S3 を切り替え)
	var driveStorage coredrive.Storage
	if serverMeta, err := metaRepo.Fetch(); err == nil {
		driveStorage = coredrive.NewStorageFromMeta(serverMeta, "./drive-files", s.config.DriveURL)
	} else {
		driveStorage = coredrive.NewLocalStorage("./drive-files", s.config.DriveURL)
	}
	driveService := coredrive.NewService(driveFileRepo, driveFolderRepo, driveStorage, idGen)

	// Image processing (Phase 4.8)
	imgProcessor := coredrive.NewDefaultImageProcessor()
	driveService.SetImageProcessor(imgProcessor)
	driveService.SetVideoProcessor(coredrive.NewFFmpegVideoProcessor(imgProcessor, nil))

	// Sensitive media detection (issue #44)
	if sensMeta, err := metaRepo.Fetch(); err == nil {
		sensCfg := coredrive.SensitiveConfig{
			Detection:            sensMeta.SensitiveMediaDetection,
			Sensitivity:          sensMeta.SensitiveMediaDetectionSensitivity,
			SetFlagAutomatically: sensMeta.SetSensitiveFlagAutomatically,
			EnableForVideos:      sensMeta.EnableSensitiveMediaDetectionForVideos,
			SilencedHosts:        sensMeta.MediaSilencedHosts,
		}
		// 外部 detection URL は config に追加可能 (未設定なら detector = nil で no-op)
		driveService.SetSensitiveDetection(nil, sensCfg)
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
	// config.URL は "https://example.com" 形式。ホスト部分だけ抽出する。
	if u, err := urlpkg.Parse(s.config.URL); err == nil {
		apRenderer.SetHost(u.Host)
	}
	apClient := activitypub.NewClient(nil, s.config.UserAgent)
	// meta.allowExternalApRedirect が false なら AP fetch でのリダイレクトを拒否する。
	if m, err := metaRepo.Fetch(); err == nil && !m.AllowExternalApRedirect {
		apClient.DisableRedirect()
	}
	apFetcher := corefederation.NewAPFetcher(apClient)
	federationResolver := corefederation.NewResolver(userRepo, noteRepo, apURLs, apFetcher, idGen)
	publickeyRepo := repository.NewUserPublickeyRepository(s.db)
	federationResolver.SetPublickeyRepo(publickeyRepo)
	federationResolver.SetPollRepo(pollRepo)
	federationProcessor := corefederation.NewProcessor(federationResolver, followingService, reactionService, noteDeleteService, userRepo, noteRepo)

	// Instance management (Phase 3 Step H)
	instanceService := coreinstance.NewService(instanceRepo, metaRepo, idGen)
	federationResolver.SetInstanceTracker(instanceService)
	// 新規 instance row 発見時に nodeinfo を取得して metadata を更新する
	instanceService.SetMetadataFetcher(coreinstance.NewFetchMetadataService(instanceRepo, apFetcher))

	// AP delivery: DeliverService + フック登録 + asynq processor 登録
	deliverService := corefederation.NewDeliverService(s.queueClient, userRepo, followingRepo, keypairRepo, apURLs)
	deliverService.SetHostBlockChecker(instanceService)

	// Relay 連携 (#161): relay actor system account + relay.Service。
	// relay.Service は AP delivery を必要とするので deliverService 構築の
	// 直後でセットアップする。admin/relays ハンドラへ注入し、Accept/Reject
	// inbox 処理で status を更新できるように processor にも渡す。
	sysAcctSvc := coresystemaccount.NewService(userRepo, keypairRepo, repository.NewSystemAccountRepository(s.db), idGen)
	relaySvc := corerelay.NewService(repository.NewRelayRepository(s.db), sysAcctSvc, apRenderer, deliverService, idGen)

	noteDeliveryHook := corefederation.NewNoteDeliveryHook(deliverService, apRenderer, apURLs, idGen, userRepo, noteRepo)
	noteDeliveryHook.SetRelayBroadcaster(relaySvc)
	noteCreateService.SetFederationHook(noteDeliveryHook)
	followingService.SetFederationHook(corefederation.NewFollowingDeliveryHook(deliverService, apRenderer, apURLs))
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
	rateLimiter := middleware.NewRedisRateLimiter(
		s.redis.Default,
		s.config.EnableIPRateLimit,
		middleware.DefaultEndpointLimits,
	)
	api.Use(rateLimiter.Middleware())

	// Meta endpoint (public)
	metaHandler := meta.NewHandler(s.config, metaRepo)
	metaHandler.SetAdRepo(repository.NewAdRepository(s.db))
	proxyAccountResolver := newProxyAccountResolver(repository.NewSystemAccountRepository(s.db), userRepo)
	metaHandler.SetProxyAccountResolver(proxyAccountResolver)
	api.POST("/meta", metaHandler.Meta)
	api.POST("/ping", metaHandler.Ping)

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
		urlPreviewFetcher := coreurlpreview.NewFetcher(previewCfg, s.redis.Default)
		urlHandler := apiurl.NewHandler(urlPreviewFetcher)
		s.echo.GET("/url", urlHandler.Preview)
	}

	// Test-only endpoints — TestMode=true のときだけ公開する。
	// Cypress の resetState コマンドが依存する /api/reset-db はここで登録する。
	// 本番で絶対に有効化してはならない (config.go 側で起動時 warning を出す)。
	if s.config.TestMode {
		testHandler := apitest.NewHandler(s.db, s.redis.Default, true)
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
	var captchaSvc *corecaptcha.Service
	if serverMeta, err := metaRepo.Fetch(); err == nil {
		captchaSvc = corecaptcha.NewService(serverMeta)
	}

	// Signup (public)
	signupHandler := apisignup.NewHandler(signupService, metaRepo, idGen)
	if captchaSvc != nil {
		signupHandler.SetCaptcha(captchaSvc)
	}
	signupHandler.SetTicketStore(&gormTicketStore{db: s.db})
	signupHandler.SetTestMode(s.config.TestMode)
	api.POST("/signup", signupHandler.Signup)

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
		resetHandler.SetEmailSender(func(to, subject, body string) {
			miscsmtp.Send(host, port, smtpUser, smtpPass, fromAddr, to, subject, body)
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

	// Notes endpoints
	notesHandler := notes.NewHandler(noteRepo, noteCreateService, noteDeleteService, noteQueryService, timelineService, reactionService, pollService, searchService, idGen)
	notesHandler.SetDriveFileRepo(driveFileRepo)
	notesHandler.SetNoteReactionRepo(reactionRepo)
	notesHandler.SetChannelRepo(channelRepo)
	notesHandler.SetChannelMutingRepo(channelMutingRepo)
	if m, err := metaRepo.Fetch(); err == nil {
		notesHandler.SetUGCVisibility(m.UgcVisibilityForVisitor)
		if m.DeeplAuthKey != nil && *m.DeeplAuthKey != "" {
			notesHandler.SetTranslator(coretranslate.NewDeepL(*m.DeeplAuthKey, m.DeeplIsPro))
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
	usersHandler.SetClipRepo(clipRepo)
	usersHandler.SetFlashRepo(flashRepo)
	usersHandler.SetGalleryRepo(repository.NewGalleryRepository(s.db))
	usersHandler.SetPageRepo(pageRepo)
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
	// SMTP メール送信を i/update-email 用に注入する。meta の SMTP 設定に従う。
	if smtpMeta, err := metaRepo.Fetch(); err == nil && smtpMeta.EnableEmail && smtpMeta.Email != nil && smtpMeta.SmtpHost != nil {
		fromAddr := *smtpMeta.Email
		host := *smtpMeta.SmtpHost
		port := 587
		if smtpMeta.SmtpPort != nil {
			port = *smtpMeta.SmtpPort
		}
		smtpUser, smtpPass := smtpMeta.SmtpUser, smtpMeta.SmtpPass
		iHandler.SetEmailSender(func(to, subject, body string) {
			miscsmtp.Send(host, port, smtpUser, smtpPass, fromAddr, to, subject, body)
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
	// Phase 7-3 (#245): pinnedNoteIds / pinnedNotes / pinnedPageId / pinnedPage
	iHandler.SetPiningRepo(piningRepo)
	iHandler.SetNoteRepo(noteRepo)
	iHandler.SetPageRepo(pageRepo)
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
	blockingHandler := blocking.NewHandler(blockingService)
	api.POST("/blocking/create", blockingHandler.Create, middleware.RequireAuth())
	api.POST("/blocking/delete", blockingHandler.Delete, middleware.RequireAuth())
	api.POST("/blocking/list", blockingHandler.List, middleware.RequireAuth())

	// Mute endpoints
	muteHandler := mute.NewHandler(mutingService)
	api.POST("/mute/create", muteHandler.Create, middleware.RequireAuth())
	api.POST("/mute/delete", muteHandler.Delete, middleware.RequireAuth())
	api.POST("/mute/list", muteHandler.List, middleware.RequireAuth())

	// Renote mute endpoints
	renoteMuteHandler := renotemute.NewHandler(renoteMutingService)
	api.POST("/renote-mute/create", renoteMuteHandler.Create, middleware.RequireAuth())
	api.POST("/renote-mute/delete", renoteMuteHandler.Delete, middleware.RequireAuth())
	api.POST("/renote-mute/list", renoteMuteHandler.List, middleware.RequireAuth())

	// Drive endpoints
	driveHandler := drive.NewHandler(driveService, idGen)
	driveHandler.SetRepos(driveFileRepo, driveFolderRepo, noteRepo)
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
	// MIME type はファイル内容の先頭から自動判定する。
	s.echo.GET("/files/:accessKey", func(c echo.Context) error {
		key := c.Param("accessKey")
		body, err := driveStorage.Get(key)
		if err != nil {
			return c.NoContent(http.StatusNotFound)
		}
		defer body.Close()

		// 先頭512バイトを読んでMIME typeを判定
		buf := make([]byte, 512)
		n, _ := body.Read(buf)
		contentType := http.DetectContentType(buf[:n])
		// 読んだ分 + 残りを連結して配信
		mr := io.MultiReader(bytes.NewReader(buf[:n]), body)
		return c.Stream(http.StatusOK, contentType, mr)
	})

	// Media proxy endpoint
	proxyAllowlist := coremediaproxy.NewDBAllowlistChecker(s.db)
	proxyService := coremediaproxy.NewService(
		s.config.URL, s.config.UserAgent, driveStorage,
		proxyAllowlist, s.config.MediaProxySecret,
		s.config.AllowedPrivateNetworks,
	)
	proxyHandler := apiproxy.NewHandler(proxyService, s.config)
	s.echo.GET("/proxy/*", proxyHandler.Handle)

	// ActivityPub resource endpoints
	apHandler := ap.NewHandler(apRenderer, userService, noteQueryService, keypairRepo, idGen)
	apHandler.SetRemote(apFetcher, federationResolver)
	s.echo.GET("/users/:id", apHandler.User)
	s.echo.GET("/notes/:id", apHandler.Note)
	// Content-negotiated actor endpoint: /@username (and /@username@host).
	// When the caller asks for application/activity+json we return the
	// Person document; otherwise the catch-all serves the HTML frontend.
	s.echo.GET("/@:acct", apHandler.UserByAcct)

	// Discovery endpoints
	wellknownHandler := wellknown.NewHandler(apURLs, userService, s.config.Host, s.config.URL)
	s.echo.GET("/.well-known/webfinger", wellknownHandler.Webfinger)
	s.echo.GET("/.well-known/host-meta", wellknownHandler.HostMeta)
	s.echo.GET("/.well-known/host-meta.json", wellknownHandler.HostMetaJSON)
	s.echo.GET("/.well-known/nodeinfo", wellknownHandler.NodeInfoDiscovery)
	s.echo.GET("/.well-known/oauth-authorization-server", wellknownHandler.OAuthAuthorizationServer)

	nodeinfoHandler := nodeinfo.NewHandler(s.config)
	s.echo.GET("/nodeinfo/2.1", nodeinfoHandler.Version2_1)

	// Inbox endpoints
	inboxHandler := inbox.NewHandler(federationResolver, federationProcessor)
	inboxHandler.SetHostBlockChecker(instanceService)
	inboxHandler.SetInstanceTracker(instanceService)
	inboxHandler.SetChartHook(chartHooks)
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
	api.POST("/federation/stats", federationHandler.Stats)
	api.POST("/federation/update-remote-user", federationHandler.UpdateRemoteUser, middleware.RequireModerator(roleService))

	// Channels endpoints (Phase 4.2)
	channelsHandler := apichannels.NewHandler(channelService, idGen)
	channelsHandler.SetFavoriteRepo(channelFavoriteRepo)
	channelsHandler.SetMutingRepo(channelMutingRepo)
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

	// Antennas endpoints (Phase 4.3)
	antennasHandler := antennas.NewHandler(antennaService, noteRepo, idGen)
	api.POST("/antennas/create", antennasHandler.Create, middleware.RequireAuth())
	api.POST("/antennas/show", antennasHandler.Show, middleware.RequireAuth())
	api.POST("/antennas/update", antennasHandler.Update, middleware.RequireAuth())
	api.POST("/antennas/delete", antennasHandler.Delete, middleware.RequireAuth())
	api.POST("/antennas/list", antennasHandler.List, middleware.RequireAuth())
	api.POST("/antennas/notes", antennasHandler.Notes, middleware.RequireAuth())

	// Clips endpoints (Phase 4.4)
	clipsHandler := clips.NewHandler(clipService, idGen)
	clipsHandler.SetFavoriteRepo(clipFavoriteRepo)
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
	flashHandler := apiflash.NewHandler(flashService)
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
	streamRegistry.Register("serverStats", channels.NewServerStats)
	streamRegistry.Register("queueStats", channels.NewQueueStats)

	// 3. Connection manager + 各 publisher の生成
	streamManager := stream.NewManager(streamRegistry, streamBus)
	// readNotificationメッセージをnotificationServiceに橋渡しする
	streamManager.SetNotificationReader(&notifReaderAdapter{svc: notificationService})
	notePublisher := stream.NewNotePublisher(streamPubSub, idGen)
	notificationPublisher := stream.NewNotificationPublisher(streamPubSub)
	notificationPublisher.SetRepos(userRepo, noteRepo, idGen)
	drivePublisher := stream.NewDrivePublisher(streamPubSub)
	reversiPublisher := stream.NewReversiGamePublisher(streamPubSub)
	mainStreamPublisher := stream.NewMainStreamPublisher(streamPubSub)

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
	// CherryPick互換AP連合: リモートユーザー宛DMをMisskey:ChatMessageで配送
	chatService.SetAPDelivery(userRepo, apRenderer, apURLs, deliverService)
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

	// 5. /streaming エンドポイント配線
	streamingHandler := streaming.NewHandler(streamManager)
	s.echo.GET("/streaming", streamingHandler.Stream)

	// Charts API endpoints (engines + hooks already wired earlier).
	chartsHandler := apicharts.NewHandler(chartCharts, nil)
	api.POST("/charts/notes", chartsHandler.Notes)
	api.POST("/charts/users", chartsHandler.Users)
	api.POST("/charts/drive", chartsHandler.Drive)
	api.POST("/charts/federation", chartsHandler.Federation)
	api.POST("/charts/instance", chartsHandler.Instance)
	api.POST("/charts/ap-request", chartsHandler.ApRequest)
	api.POST("/charts/active-users", chartsHandler.ActiveUsers)
	api.POST("/charts/user/notes", chartsHandler.UserNotes)
	api.POST("/charts/user/drive", chartsHandler.UserDrive)
	api.POST("/charts/user/following", chartsHandler.UserFollowing)
	api.POST("/charts/user/pv", chartsHandler.UserPv)
	api.POST("/charts/user/reactions", chartsHandler.UserReactions)

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
	adminHandler.SetAbuseRepo(abuseReportRepo)
	adminHandler.SetAbuseForwarder(coreabuse.NewForwarder(abuseReportRepo, sysAcctSvc, apRenderer, deliverService))
	adminHandler.SetDeleteAccountEnqueuer(s.queueClient)
	adminHandler.SetPasswordResetRepo(resetReqRepo)
	adminHandler.SetServerURL(s.config.URL)
	adminHandler.SetConfigSetupPassword(s.config.SetupPassword)
	if smtpMetaAdmin, err := metaRepo.Fetch(); err == nil && smtpMetaAdmin.EnableEmail && smtpMetaAdmin.Email != nil && smtpMetaAdmin.SmtpHost != nil {
		fromAddr := *smtpMetaAdmin.Email
		host := *smtpMetaAdmin.SmtpHost
		port := 587
		if smtpMetaAdmin.SmtpPort != nil {
			port = *smtpMetaAdmin.SmtpPort
		}
		smtpUser, smtpPass := smtpMetaAdmin.SmtpUser, smtpMetaAdmin.SmtpPass
		adminHandler.SetEmailSender(func(to, subject, body string) {
			miscsmtp.Send(host, port, smtpUser, smtpPass, fromAddr, to, subject, body)
		})
	}
	adminHandler.SetModLogRepo(modLogRepo)
	adminHandler.SetEmojiRepo(emojiRepo)
	adminHandler.SetDriveFileRepo(driveFileRepo)
	adminHandler.SetAdminDB(s.db)
	adminHandler.SetUserIPRepo(userIPRepo)
	adminHandler.SetEmojiImportEnqueuer(s.queueClient)
	adminHandler.SetRelayService(relaySvc)
	adminHandler.SetSystemWebhookRepo(systemWebhookRepo)
	adminHandler.SetRecipientRepo(recipientRepo)
	adminHandler.SetAdRepo(repository.NewAdRepository(s.db))
	adminHandler.SetAvatarDecorationRepo(repository.NewAvatarDecorationRepository(s.db))
	adminHandler.SetInviteRepo(repository.NewRegistrationTicketRepository(s.db))
	adminHandler.SetPromoNoteRepo(repository.NewPromoNoteRepository(s.db))
	adminHandler.SetNoteFinder(noteRepo)
	if s.queueInspector != nil {
		adminHandler.SetQueueInspector(&queueInspectorAdapter{inner: s.queueInspector})
	}
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
	api.POST("/ap/get", apHandler.APIGet, middleware.RequireAdmin(roleService))
	api.POST("/ap/show", apHandler.APIShow, middleware.RequireAuth())
	api.POST("/ap/notes", apHandler.APNotes)

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

	// get-online-users-count — オンラインユーザー数
	api.POST("/get-online-users-count", func(c echo.Context) error {
		count, _ := userRepo.CountOnlineUsers()
		return c.JSON(http.StatusOK, map[string]any{"count": count})
	})

	// server-info (公開版) — サーバー情報
	api.POST("/server-info", func(c echo.Context) error {
		if m, err := metaRepo.Fetch(); err == nil && m.EnableServerMachineStats {
			return c.JSON(http.StatusOK, serverstats.Collect())
		}
		return c.JSON(http.StatusOK, serverstats.Empty())
	})

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

	// fetch-rss — RSS parser (stub)
	api.POST("/fetch-rss", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"items": []any{}})
	})

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
	s.echo.GET("/*", frontendHTML(s.config, metaRepo, proxyAccountResolver))
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

// gormTicketStore implements apisignup.TicketStore using GORM.
type gormTicketStore struct {
	db *gorm.DB
}

func (s *gormTicketStore) FindByCode(code string) (*model.RegistrationTicket, error) {
	var ticket model.RegistrationTicket
	if err := s.db.Where(`"code" = ?`, code).First(&ticket).Error; err != nil {
		return nil, err
	}
	return &ticket, nil
}

func (s *gormTicketStore) MarkUsed(ticketID, userID string) error {
	now := time.Now()
	return s.db.Model(&model.RegistrationTicket{}).Where(`"id" = ?`, ticketID).Updates(map[string]any{
		"usedById": userID,
		"usedAt":   now,
	}).Error
}

// notifReaderAdapter bridges stream.NotificationReader to
// core/notification.Service.MarkAllAsRead.
type notifReaderAdapter struct {
	svc *corenotification.Service
}

func (a *notifReaderAdapter) ReadAll(userID string) error {
	return a.svc.MarkAllAsRead(context.Background(), userID)
}
