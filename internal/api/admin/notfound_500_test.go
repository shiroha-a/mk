package admin_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	apiadmin "github.com/shiroha-a/mk/internal/api/admin"
	"github.com/shiroha-a/mk/internal/core/role"
	"github.com/shiroha-a/mk/internal/core/signup"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
)

// failingUserRepo makes every FindByID look like a database failure rather
// than a missing row.
type failingUserRepo struct {
	*testutil.MockUserRepository
	err error
}

func (r *failingUserRepo) FindByID(string) (*model.User, error) { return nil, r.err }

// handlerWithFailingUserRepo builds an admin handler whose user lookups fail
// with a database error.
func handlerWithFailingUserRepo(t *testing.T, err error) *apiadmin.Handler {
	t.Helper()
	userRepo := &failingUserRepo{MockUserRepository: testutil.NewMockUserRepository(), err: err}
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x"}
	roleRepo := testutil.NewMockRoleRepository()
	assignRepo := testutil.NewMockRoleAssignmentRepository(roleRepo)
	idGen, _ := id.NewGenerator("aidx")
	return apiadmin.NewHandler(
		signup.NewService(userRepo, metaRepo, idGen),
		role.NewService(roleRepo, assignRepo, metaRepo, idGen),
		metaRepo, userRepo, idGen,
	)
}

// **DB 障害を not-found に丸めないこと** (#2792)。
//
// repository は GORM の error をそのまま返すので、`err != nil` をまとめて
// not-found 扱いにすると接続断まで「そんな利用者は居ない」になる。クライアント
// からは区別できず、監視でも 5xx が立たない。
//
// upstream は `.findOneBy` の結果が `null` かどうかで判定するので、DB 障害は
// 例外として 500 に化ける。こちらもそれに合わせる。
func TestAdmin_DBFailureIsNot4xx(t *testing.T) {
	dbErr := errors.New("dial tcp 127.0.0.1:5432: connect: connection refused")

	h := handlerWithFailingUserRepo(t, dbErr)

	for _, tt := range []struct {
		name string
		body string
		run  func() int
	}{
		{"admin/show-user", `{"userId":"u1"}`, func() int {
			return doPost(h.ShowUser, `{"userId":"u1"}`, adminUser).Code
		}},
		{"admin/suspend-user", `{"userId":"u1"}`, func() int {
			return doPost(h.SuspendUser, `{"userId":"u1"}`, adminUser).Code
		}},
		{"admin/unsuspend-user", `{"userId":"u1"}`, func() int {
			return doPost(h.UnsuspendUser, `{"userId":"u1"}`, adminUser).Code
		}},
		{"admin/reset-password", `{"userId":"u1"}`, func() int {
			return doPost(h.ResetPassword, `{"userId":"u1"}`, adminUser).Code
		}},
		{"admin/unset-mfa", `{"userId":"u1"}`, func() int {
			return doPost(h.UnsetMfa, `{"userId":"u1"}`, adminUser).Code
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, http.StatusInternalServerError, tt.run(),
				"DB 障害が 4xx に化けている (#2792)")
		})
	}

	// promo/create は user ではなく note を引く。**別の repo なので上の
	// failingUserRepo では守れない** (実際、guard を外す変異が生き残った)。
	t.Run("admin/promo/create", func(t *testing.T) {
		ph, _, _, _ := newTestHandler(t)
		ph.SetPromoNoteRepo(&stubPromoRepo{})
		ph.SetNoteFinder(failingNoteFinder{err: dbErr})

		rec := doPost(ph.PromoCreate, `{"noteId":"n1","expiresAt":1}`, adminUser)
		assert.Equal(t, http.StatusInternalServerError, rec.Code,
			"DB 障害が 4xx に化けている (#2792)")
	})
}

// failingNoteFinder makes every note lookup look like a database failure.
type failingNoteFinder struct{ err error }

func (f failingNoteFinder) FindByID(string) (*model.Note, error) { return nil, f.err }
