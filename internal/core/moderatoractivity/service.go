// Package moderatoractivity implements the upstream
// CheckModeratorsActivityProcessorService (#1563): it detects when every
// moderator has been inactive past a threshold and, if so, flips the instance
// to invitation-only (disableRegistration=true), warning moderators by email /
// announcement / SystemWebhook as the deadline approaches.
package moderatoractivity

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/shiroha-a/mk/internal/model"
)

const (
	// inactivityLimitDays は全モデレーターが非アクティブと判定する日数閾値。
	inactivityLimitDays = 7
	// warningRemainingDays は警告通知を始める「残り日数」。
	warningRemainingDays = 2
	// warningIntervalHours は警告通知の間隔 (残り時間がこの倍数のときだけ送る)。
	warningIntervalHours = 6
)

// Narrow dependency interfaces (satisfied by the wired services/repos).
type (
	// ModeratorLister returns moderators+admins+root (core/role.Service.GetModerators).
	ModeratorLister interface {
		GetModerators() ([]*model.User, error)
	}
	// MetaService reads and updates instance meta (repository.MetaRepository).
	MetaService interface {
		Fetch() (*model.Meta, error)
		Update(fields map[string]any) error
	}
	// ProfileLookup batch-resolves user profiles for email delivery
	// (repository.UserRepository.FindProfilesByUserIDs).
	ProfileLookup interface {
		FindProfilesByUserIDs(userIDs []string) ([]*model.UserProfile, error)
	}
	// AnnouncementCreator persists an announcement (repository.AnnouncementRepository).
	AnnouncementCreator interface {
		Create(a *model.Announcement) error
	}
	// WebhookDispatcher fans an event out to subscribed system webhooks
	// (core/webhook.Service.DispatchSystem).
	WebhookDispatcher interface {
		DispatchSystem(eventType string, body any)
	}
	// IDGenerator generates announcement ids (misc/id.Generator).
	IDGenerator interface {
		Generate(t time.Time) string
	}
)

// EmailSender sends a plaintext email. nil disables email delivery.
type EmailSender func(to, subject, body string)

// Service evaluates moderator activity and applies the invitation-only switch
// plus notifications. Any dependency may be nil to disable its effect.
type Service struct {
	moderators ModeratorLister
	meta       MetaService
	profiles   ProfileLookup
	announce   AnnouncementCreator
	webhook    WebhookDispatcher
	idGen      IDGenerator
	sendEmail  EmailSender
	now        func() time.Time
}

// NewService constructs the moderator-activity service.
func NewService(
	moderators ModeratorLister,
	meta MetaService,
	profiles ProfileLookup,
	announce AnnouncementCreator,
	webhook WebhookDispatcher,
	idGen IDGenerator,
	sendEmail EmailSender,
) *Service {
	return &Service{
		moderators: moderators,
		meta:       meta,
		profiles:   profiles,
		announce:   announce,
		webhook:    webhook,
		idGen:      idGen,
		sendEmail:  sendEmail,
		now:        time.Now,
	}
}

// Check runs one evaluation cycle. Safe to call when dependencies are missing
// (returns nil). Mirrors upstream process()/processImpl().
func (s *Service) Check() error {
	if s.moderators == nil || s.meta == nil {
		return nil
	}
	meta, err := s.meta.Fetch()
	if err != nil {
		return err
	}
	// 既に招待制なら何もしない (upstream process() の早期 return)。
	if meta.DisableRegistration {
		return nil
	}

	mods, err := s.moderators.GetModerators()
	if err != nil {
		return err
	}
	// allMods = 通知対象 (= 全 moderator)。upstream の notify* は lastActiveDate で
	// filter せず fetchModerators() で全件に通知するため、警告 / 招待制切替の通知は
	// lastActiveDate が null の moderator (= mk-go では認証 API を叩いていないだけの
	// 現役 panel admin になりがち) にも届ける必要がある。
	// withDate = 非アクティブ判定対象 (upstream 同様 lastActiveDate==null は除外)。
	allMods := make([]*model.User, 0, len(mods))
	withDate := make([]*model.User, 0, len(mods))
	for _, m := range mods {
		if m == nil {
			continue
		}
		allMods = append(allMods, m)
		if m.LastActiveDate != nil {
			withDate = append(withDate, m)
		}
	}
	// 安全側 divergence: lastActiveDate を持つ moderator が 0 人なら何もしない。
	// upstream は空集合で isModeratorsInactive=true となり登録を無効化しうるが、
	// mk-go の lastActiveDate は認証 API request 時にしか bump されないため、
	// 全 null = 活動を計測できていないだけ とみなし、fresh/quiet instance を
	// 誤って招待制にしない (#1563)。
	if len(withDate) == 0 {
		return nil
	}

	now := s.now()
	// upstream は setDate(today-7) の calendar-day 減算 (server local TZ)。mk-go は
	// 168h の絶対減算で、UTC 運用では同値。DST observing TZ の遷移日のみ ±1h ずれる
	// が、運用上無視できる軽微差 (#1563)。
	inactivePeriod := now.Add(-inactivityLimitDays * 24 * time.Hour)
	inactiveCount := 0
	var newest time.Time
	for _, m := range withDate {
		if m.LastActiveDate.Before(inactivePeriod) {
			inactiveCount++
		}
		if m.LastActiveDate.After(newest) {
			newest = *m.LastActiveDate
		}
	}
	isModeratorsInactive := inactiveCount == len(withDate)

	if isModeratorsInactive {
		slog.Warn("checkModeratorsActivity: all moderators inactive, switching to invitation-only",
			"limitDays", inactivityLimitDays)
		if err := s.meta.Update(map[string]any{"disableRegistration": true}); err != nil {
			slog.Warn("checkModeratorsActivity: update meta failed", "err", err)
		}
		s.notifyChangeToInvitationOnly(allMods)
		return nil
	}

	// newest は inactivePeriod 以上 (= 少なくとも 1 人 active) なので非負。
	remaining := newest.Sub(inactivePeriod)
	remainingDays := int(remaining / (24 * time.Hour)) // floor (非負)
	remainingHours := int(remaining / time.Hour)       // floor (非負)
	if remainingDays <= warningRemainingDays && remainingHours%warningIntervalHours == 0 {
		s.notifyInactiveModeratorsWarning(allMods, remainingDays, remainingHours)
	}
	return nil
}

// verifiedEmails returns the verified email addresses of the given moderators.
func (s *Service) verifiedEmails(mods []*model.User) []string {
	if s.profiles == nil {
		return nil
	}
	ids := make([]string, 0, len(mods))
	for _, m := range mods {
		ids = append(ids, m.ID)
	}
	profiles, err := s.profiles.FindProfilesByUserIDs(ids)
	if err != nil {
		slog.Warn("checkModeratorsActivity: load profiles failed", "err", err)
		return nil
	}
	emails := make([]string, 0, len(profiles))
	for _, p := range profiles {
		if p.EmailVerified && p.Email != nil && *p.Email != "" {
			emails = append(emails, *p.Email)
		}
	}
	return emails
}

// notifyInactiveModeratorsWarning emails moderators and dispatches the warning
// SystemWebhook (upstream notifyInactiveModeratorsWarning).
func (s *Service) notifyInactiveModeratorsWarning(mods []*model.User, remainingDays, remainingHours int) {
	subject := "Moderator Inactivity Warning / モデレーター不在の通知"
	timeVariant := formatRemaining(remainingDays, remainingHours, false)
	timeVariantJa := formatRemaining(remainingDays, remainingHours, true)
	body := joinLines(
		"To moderators,",
		"",
		"A moderator has been inactive for a period of time. If there are "+timeVariant+" of inactivity left, it will switch to invitation only.",
		"If you do not want it to switch to invitation only, log in to Misskey to update your last active date.",
		"",
		"To モデレーター各位",
		"",
		"モデレーターが一定期間活動していないようです。あと"+timeVariantJa+"活動していない状態が続くと招待制に切り替わります。",
		"招待制に切り替わることを望まない場合は、Misskeyにログインして最終アクティブ日時を更新してください。",
	)
	if s.sendEmail != nil {
		for _, email := range s.verifiedEmails(mods) {
			s.sendEmail(email, subject, body)
		}
	}
	if s.webhook != nil {
		s.webhook.DispatchSystem("inactiveModeratorsWarning", map[string]any{
			"remainingTime": map[string]any{
				"asDays":  remainingDays,
				"asHours": remainingHours,
			},
		})
	}
}

// notifyChangeToInvitationOnly creates a per-moderator announcement, emails
// moderators and dispatches the invitation-only SystemWebhook (upstream
// notifyChangeToInvitationOnly).
func (s *Service) notifyChangeToInvitationOnly(mods []*model.User) {
	subject := "Change to Invitation-Only / 招待制に変更されました"
	body := joinLines(
		"To moderators,",
		"",
		"Changed to invitation only because no moderator activity was detected for 7 days.",
		"To turn off invitation only, you need to access the control panel.",
		"",
		"To モデレーター各位",
		"",
		"モデレーターの活動が7日間検出されなかったため、招待制に変更されました。",
		"招待制を解除するには、コントロールパネルにアクセスする必要があります。",
	)

	if s.announce != nil && s.idGen != nil {
		for _, m := range mods {
			uid := m.ID
			a := &model.Announcement{
				ID:                     s.idGen.Generate(s.now()),
				Title:                  subject,
				Text:                   body,
				Icon:                   "info",
				Display:                "normal",
				IsActive:               true,
				ForExistingUsers:       true,
				NeedConfirmationToRead: true,
				UserID:                 &uid,
			}
			if err := s.announce.Create(a); err != nil {
				slog.Warn("checkModeratorsActivity: create announcement failed", "userId", uid, "err", err)
			}
		}
	}
	if s.sendEmail != nil {
		for _, email := range s.verifiedEmails(mods) {
			s.sendEmail(email, subject, body)
		}
	}
	if s.webhook != nil {
		s.webhook.DispatchSystem("inactiveModeratorsInvitationOnlyChanged", map[string]any{})
	}
}

// formatRemaining mirrors upstream timeVariant: "{hours} hours" when days==0,
// else "{days} days" (Japanese variant when ja=true).
func formatRemaining(days, hours int, ja bool) string {
	if days == 0 {
		if ja {
			return strconv.Itoa(hours) + "時間"
		}
		return strconv.Itoa(hours) + " hours"
	}
	if ja {
		return strconv.Itoa(days) + "日間"
	}
	return strconv.Itoa(days) + " days"
}

func joinLines(lines ...string) string {
	return strings.Join(lines, "\n")
}
