package signupapplication

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var testDB *gorm.DB

func TestMain(m *testing.M) {
	db, err := testutil.OpenTestDB()
	if err != nil {
		panic("failed to open test DB: " + err.Error())
	}
	testDB = db
	testutil.ApplyMigrations(testDB)
	os.Exit(m.Run())
}

var testContact = Contact{Host: "remote.example", RemoteID: "r1", Username: "alice"}

// newService builds a Service on a clean table with a fixed clock.
func newService(t *testing.T) (*Service, *time.Time) {
	t.Helper()
	require.NoError(t, testDB.Exec(`DELETE FROM "signup_application"`).Error)
	t.Cleanup(func() { testDB.Exec(`DELETE FROM "signup_application"`) })

	idGen, err := id.NewGenerator("aidx")
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	svc := NewService(repository.NewSignupApplicationRepository(testDB), idGen)
	svc.SetClock(func() time.Time { return now })
	return svc, &now
}

func TestApply_CreatesPending(t *testing.T) {
	svc, now := newService(t)

	app, err := svc.Apply(testContact, "  よろしくお願いします  ")
	require.NoError(t, err)
	assert.Equal(t, model.SignupApplicationPending, app.Status)
	assert.Equal(t, "remote.example", app.ContactHost)
	assert.Equal(t, "r1", app.ContactRemoteID)
	assert.Equal(t, "alice", app.ContactUsername)
	require.NotNil(t, app.Reason)
	assert.Equal(t, "よろしくお願いします", *app.Reason, "前後の空白は落とす")
	assert.Equal(t, now.Add(DefaultTTL), app.ExpiresAt)
}

func TestApply_EmptyReasonIsNil(t *testing.T) {
	svc, _ := newService(t)

	app, err := svc.Apply(testContact, "   ")
	require.NoError(t, err)
	assert.Nil(t, app.Reason)
}

func TestApply_RejectsDuplicateLive(t *testing.T) {
	svc, _ := newService(t)

	_, err := svc.Apply(testContact, "")
	require.NoError(t, err)

	_, err = svc.Apply(testContact, "")
	assert.ErrorIs(t, err, ErrLiveApplicationExists)
}

// 期限切れの申請が席を占めたままだと、本人が申請し直せない。**参照のたびに
// 掃除する**ことで、掃除ジョブが無くても回る。
func TestApply_AfterExpiryFreesTheContact(t *testing.T) {
	svc, now := newService(t)

	first, err := svc.Apply(testContact, "")
	require.NoError(t, err)

	*now = now.Add(DefaultTTL + time.Minute)

	second, err := svc.Apply(testContact, "")
	require.NoError(t, err)
	assert.NotEqual(t, first.ID, second.ID)

	// 古い方は expired に落ちていること。
	old, err := svc.Get(first.ID)
	require.NoError(t, err)
	assert.Equal(t, model.SignupApplicationExpired, old.Status)
}

func TestApply_ValidatesContact(t *testing.T) {
	svc, _ := newService(t)

	for _, tt := range []struct {
		name    string
		contact Contact
	}{
		{name: "empty host", contact: Contact{RemoteID: "r1"}},
		{name: "empty remote id", contact: Contact{Host: "remote.example"}},
		{name: "blank host", contact: Contact{Host: "   ", RemoteID: "r1"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Apply(tt.contact, "")
			assert.ErrorIs(t, err, ErrInvalidContact)
		})
	}
}

// 理由は rune 単位で数える。**byte で見ると日本語が通らなくなる。**
func TestApply_ReasonLength(t *testing.T) {
	svc, _ := newService(t)

	t.Run("multibyte at the limit is accepted", func(t *testing.T) {
		_, err := svc.Apply(testContact, strings.Repeat("あ", MaxReasonLength))
		assert.NoError(t, err)
	})

	t.Run("over the limit is rejected", func(t *testing.T) {
		_, err := svc.Apply(Contact{Host: "remote.example", RemoteID: "r2"},
			strings.Repeat("a", MaxReasonLength+1))
		assert.ErrorIs(t, err, ErrReasonTooLong)
	})
}

func TestCurrent(t *testing.T) {
	svc, now := newService(t)

	t.Run("nil when there is none", func(t *testing.T) {
		got, err := svc.Current(testContact)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	app, err := svc.Apply(testContact, "")
	require.NoError(t, err)

	t.Run("returns the live application", func(t *testing.T) {
		got, err := svc.Current(testContact)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, app.ID, got.ID)
	})

	t.Run("expires lazily", func(t *testing.T) {
		*now = now.Add(DefaultTTL)
		got, err := svc.Current(testContact)
		require.NoError(t, err)
		assert.Nil(t, got)

		stored, err := svc.Get(app.ID)
		require.NoError(t, err)
		assert.Equal(t, model.SignupApplicationExpired, stored.Status)
	})

	t.Run("validates contact", func(t *testing.T) {
		_, err := svc.Current(Contact{})
		assert.ErrorIs(t, err, ErrInvalidContact)
	})
}

func TestLatest(t *testing.T) {
	svc, _ := newService(t)

	t.Run("nil when there is none", func(t *testing.T) {
		got, err := svc.Latest(testContact)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	app, err := svc.Apply(testContact, "")
	require.NoError(t, err)
	require.NoError(t, svc.Reject(app.ID, "mod1"))

	t.Run("returns terminal applications too", func(t *testing.T) {
		got, err := svc.Latest(testContact)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, model.SignupApplicationRejected, got.Status)
	})

	t.Run("validates contact", func(t *testing.T) {
		_, err := svc.Latest(Contact{})
		assert.ErrorIs(t, err, ErrInvalidContact)
	})
}

func TestApprove(t *testing.T) {
	svc, now := newService(t)

	app, err := svc.Apply(testContact, "")
	require.NoError(t, err)

	require.NoError(t, svc.Approve(app.ID, "mod1"))

	stored, err := svc.Get(app.ID)
	require.NoError(t, err)
	assert.Equal(t, model.SignupApplicationApproved, stored.Status)
	require.NotNil(t, stored.ProcessedByID)
	assert.Equal(t, "mod1", *stored.ProcessedByID)
	require.NotNil(t, stored.ProcessedAt)
	// 承認時点ではチケットを発行しない (登録経路で発行して即消費する)。
	assert.Nil(t, stored.TicketID)

	t.Run("cannot approve twice", func(t *testing.T) {
		assert.ErrorIs(t, svc.Approve(app.ID, "mod1"), ErrNotPending)
	})

	t.Run("not found", func(t *testing.T) {
		assert.ErrorIs(t, svc.Approve("no-such-id", "mod1"), ErrNotFound)
	})

	t.Run("expired cannot be approved", func(t *testing.T) {
		other, err := svc.Apply(Contact{Host: "remote.example", RemoteID: "r9"}, "")
		require.NoError(t, err)
		*now = now.Add(DefaultTTL)
		// **ErrNotPending ではなく ErrExpired。** 掃除は遅延反映なので行は
		// pending のまま残っており、「審査待ちに見えるのに審査待ちではない」
		// という説明になってしまう。
		assert.ErrorIs(t, svc.Approve(other.ID, "mod1"), ErrExpired)

		// 実態へ寄せてあること。
		stored, err := svc.Get(other.ID)
		require.NoError(t, err)
		assert.Equal(t, model.SignupApplicationExpired, stored.Status)
		assert.Nil(t, stored.ProcessedByID, "期限切れは審査の結果ではない")
	})
}

func TestReject(t *testing.T) {
	svc, now := newService(t)

	app, err := svc.Apply(testContact, "")
	require.NoError(t, err)

	require.NoError(t, svc.Reject(app.ID, "mod2"))

	stored, err := svc.Get(app.ID)
	require.NoError(t, err)
	assert.Equal(t, model.SignupApplicationRejected, stored.Status)
	require.NotNil(t, stored.ProcessedByID)
	assert.Equal(t, "mod2", *stored.ProcessedByID)

	t.Run("cannot reject twice", func(t *testing.T) {
		assert.ErrorIs(t, svc.Reject(app.ID, "mod2"), ErrNotPending)
	})

	t.Run("not found", func(t *testing.T) {
		assert.ErrorIs(t, svc.Reject("no-such-id", "mod2"), ErrNotFound)
	})

	// 期限切れは却下ではなく期限切れのまま残す。**審査していないものを
	// 「審査して落とした」と記録しない。**
	t.Run("expired cannot be rejected", func(t *testing.T) {
		other, err := svc.Apply(Contact{Host: "remote.example", RemoteID: "r8"}, "")
		require.NoError(t, err)
		*now = now.Add(DefaultTTL)
		assert.ErrorIs(t, svc.Reject(other.ID, "mod2"), ErrExpired)

		stored, err := svc.Get(other.ID)
		require.NoError(t, err)
		assert.Equal(t, model.SignupApplicationExpired, stored.Status)
		assert.Nil(t, stored.ProcessedByID, "却下として記録しないこと")
	})
}

func TestMarkCompleted(t *testing.T) {
	svc, now := newService(t)

	app, err := svc.Apply(testContact, "")
	require.NoError(t, err)

	t.Run("pending cannot be completed", func(t *testing.T) {
		assert.ErrorIs(t, svc.MarkCompleted(app.ID, "u1", "t1"), ErrNotApproved)
	})

	require.NoError(t, svc.Approve(app.ID, "mod1"))
	require.NoError(t, svc.MarkCompleted(app.ID, "u1", "t1"))

	stored, err := svc.Get(app.ID)
	require.NoError(t, err)
	assert.Equal(t, model.SignupApplicationCompleted, stored.Status)
	require.NotNil(t, stored.UsedByID)
	assert.Equal(t, "u1", *stored.UsedByID)
	require.NotNil(t, stored.TicketID)
	assert.Equal(t, "t1", *stored.TicketID)

	t.Run("cannot complete twice", func(t *testing.T) {
		assert.ErrorIs(t, svc.MarkCompleted(app.ID, "u1", "t1"), ErrNotApproved)
	})

	t.Run("not found", func(t *testing.T) {
		assert.ErrorIs(t, svc.MarkCompleted("no-such-id", "u1", "t1"), ErrNotFound)
	})

	t.Run("expired cannot be completed", func(t *testing.T) {
		other, err := svc.Apply(Contact{Host: "remote.example", RemoteID: "r7"}, "")
		require.NoError(t, err)
		require.NoError(t, svc.Approve(other.ID, "mod1"))
		*now = now.Add(DefaultTTL)
		assert.ErrorIs(t, svc.MarkCompleted(other.ID, "u2", "t2"), ErrExpired)
	})

	t.Run("empty ticket id is not recorded", func(t *testing.T) {
		svc2, _ := newService(t)
		a, err := svc2.Apply(testContact, "")
		require.NoError(t, err)
		require.NoError(t, svc2.Approve(a.ID, "mod1"))
		require.NoError(t, svc2.MarkCompleted(a.ID, "u3", ""))

		stored, err := svc2.Get(a.ID)
		require.NoError(t, err)
		assert.Nil(t, stored.TicketID)
	})
}

func TestGet_NotFound(t *testing.T) {
	svc, _ := newService(t)
	_, err := svc.Get("no-such-id")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestListAndCount(t *testing.T) {
	svc, _ := newService(t)

	pending, err := svc.Apply(Contact{Host: "remote.example", RemoteID: "r1"}, "")
	require.NoError(t, err)
	approved, err := svc.Apply(Contact{Host: "remote.example", RemoteID: "r2"}, "")
	require.NoError(t, err)
	require.NoError(t, svc.Approve(approved.ID, "mod1"))
	rejected, err := svc.Apply(Contact{Host: "remote.example", RemoteID: "r3"}, "")
	require.NoError(t, err)
	require.NoError(t, svc.Reject(rejected.ID, "mod1"))

	rows, err := svc.List(repository.SignupApplicationFilterPending, 50, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, pending.ID, rows[0].ID)

	n, err := svc.Count(repository.SignupApplicationFilterAll)
	require.NoError(t, err)
	assert.Equal(t, 3, n)
}

func TestExpireStale(t *testing.T) {
	svc, now := newService(t)

	app, err := svc.Apply(testContact, "")
	require.NoError(t, err)

	changed, err := svc.ExpireStale()
	require.NoError(t, err)
	assert.Equal(t, 0, changed, "期限内は触らない")

	*now = now.Add(DefaultTTL)
	changed, err = svc.ExpireStale()
	require.NoError(t, err)
	assert.Equal(t, 1, changed)

	stored, err := svc.Get(app.ID)
	require.NoError(t, err)
	assert.Equal(t, model.SignupApplicationExpired, stored.Status)
}

// SetTTL / SetClock は不正な値を無視する。**設定ミスで全申請が即座に期限切れに
// なる、という壊れ方をさせない。**
func TestSetters_IgnoreInvalidValues(t *testing.T) {
	svc, now := newService(t)

	svc.SetTTL(0)
	svc.SetTTL(-time.Hour)
	app, err := svc.Apply(testContact, "")
	require.NoError(t, err)
	assert.Equal(t, now.Add(DefaultTTL), app.ExpiresAt)

	svc.SetClock(nil)
	got, err := svc.Current(testContact)
	require.NoError(t, err)
	require.NotNil(t, got, "clock が nil で潰れていないこと")
}

func TestSetTTL(t *testing.T) {
	svc, now := newService(t)
	svc.SetTTL(time.Hour)

	app, err := svc.Apply(testContact, "")
	require.NoError(t, err)
	assert.Equal(t, now.Add(time.Hour), app.ExpiresAt)
}
