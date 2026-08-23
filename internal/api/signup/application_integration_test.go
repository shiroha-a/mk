package signup_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	apisignup "github.com/shiroha-a/mk/internal/api/signup"
	corerole "github.com/shiroha-a/mk/internal/core/role"
	coresignup "github.com/shiroha-a/mk/internal/core/signup"
	"github.com/shiroha-a/mk/internal/core/signupapplication"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// **ハンドラが tx 経路を通っていることを DB 込みで確かめる (#2580)。** mock 構成では
// SignupForApplication が通常の Signup にフォールバックするので、配線を間違えても
// 単体テストでは気づけない。
func newApprovalHandlerWithDB(t *testing.T) (*apisignup.Handler, *gorm.DB, *model.SignupApplication) {
	t.Helper()
	db, err := testutil.OpenTestDB()
	if err != nil {
		t.Skipf("PostgreSQL test DB unavailable: %v", err)
	}
	testutil.ApplyMigrations(db)

	idGen, _ := id.NewGenerator("aidx")
	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{ID: "x", ApprovalRequiredForSignup: true}

	appRepo := repository.NewSignupApplicationRepository(db)
	svc := coresignup.NewService(repository.NewUserRepository(db), metaRepo, idGen)
	svc.SetUserPendingRepo(repository.NewUserPendingRepository(db))
	svc.SetTicketRepo(repository.NewRegistrationTicketRepository(db))
	svc.SetSignupApplicationRepo(appRepo)
	svc.SetDB(db)

	h := apisignup.NewHandler(svc, metaRepo, idGen)
	h.SetSignupApplications(signupapplication.NewService(appRepo, idGen))

	now := time.Now()
	app := &model.SignupApplication{
		ID:            "itapi_app1",
		ClaimCodeHash: signupapplication.HashClaimCode("itapi-code"),
		Status:        model.SignupApplicationApproved,
		Answers:       []byte(`[]`),
		CreatedAt:     now,
		UpdatedAt:     now,
		ExpiresAt:     now.Add(24 * time.Hour),
	}
	require.NoError(t, db.Create(app).Error)

	t.Cleanup(func() {
		db.Exec(`DELETE FROM "user_profile" WHERE "userId" IN (SELECT id FROM "user" WHERE "usernameLower" LIKE 'itapi%')`)
		db.Exec(`DELETE FROM "user_keypair" WHERE "userId" IN (SELECT id FROM "user" WHERE "usernameLower" LIKE 'itapi%')`)
		db.Exec(`DELETE FROM "user_keypair_extra" WHERE "userId" IN (SELECT id FROM "user" WHERE "usernameLower" LIKE 'itapi%')`)
		db.Exec(`DELETE FROM "user" WHERE "usernameLower" LIKE 'itapi%'`)
		db.Exec(`DELETE FROM "used_username" WHERE username LIKE 'itapi%'`)
		db.Exec(`DELETE FROM "signup_application" WHERE id LIKE 'itapi%'`)
	})
	return h, db, app
}

// 承認済みの申請者が**別々の username で同時に**登録しても、作れるアカウントは 1 つ。
//
// **逐次では突けない。** 2 回目は handler 冒頭の状態確認 (申請が completed に
// なっている) で弾かれるので、競合を再現するには同時に投げる必要がある。
func TestApplicationRegister_ImmediateConcurrentCallsCreateOneAccount(t *testing.T) {
	h, db, app := newApprovalHandlerWithDB(t)

	const n = 4
	start := make(chan struct{})
	codes := make(chan int, n)
	bodies := make(chan string, n)
	for i := range n {
		go func(i int) {
			<-start
			rec := doPost(h.ApplicationRegister, fmt.Sprintf(
				`{"claimCode":"itapi-code","username":"itapic%d","password":"hunter22"}`, i))
			codes <- rec.Code
			bodies <- rec.Body.String()
		}(i)
	}
	close(start)

	ok := 0
	for range n {
		if <-codes == http.StatusOK {
			ok++
		}
		body := <-bodies
		if !strings.Contains(body, "token") {
			// 負けた側は NOT_APPROVED。**500 に落ちると原因から遠い症状になる。**
			assert.Contains(t, body, "NOT_APPROVED", body)
		}
	}
	assert.Equal(t, 1, ok, "200 が返るのは 1 つだけ")

	var count int64
	require.NoError(t, db.Model(&model.User{}).
		Where(`"usernameLower" LIKE ?`, "itapic%").Count(&count).Error)
	assert.Equal(t, int64(1), count, "作られたアカウントも 1 つだけ")

	var stored model.SignupApplication
	require.NoError(t, db.Where("id = ?", app.ID).First(&stored).Error)
	assert.Equal(t, model.SignupApplicationCompleted, stored.Status)
}

// レスポンスの形が壊れていないこと (tx 経路でも profile を返す)。
func TestApplicationRegister_ImmediateResponseShape(t *testing.T) {
	h, _, _ := newApprovalHandlerWithDB(t)

	rec := doPost(h.ApplicationRegister,
		`{"claimCode":"itapi-code","username":"itapishape","password":"hunter22"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	resp := parseResp(t, rec)
	assert.NotEmpty(t, resp["id"])
	assert.NotEmpty(t, resp["token"])
	// MeDetailed そのものを返す契約。profile 由来の field が欠けていないこと。
	assert.Contains(t, resp, "injectFeaturedNote")
	assert.Contains(t, resp, "autoAcceptFollowed")
}

// #2673: 承認制の登録経路も実効 policy を返す。signup と register の 2 経路が
// あり、片方だけ直すのがこの種の修正で最もありがちな取りこぼしなので、
// register 側も独立に固定する。
func TestApplicationRegister_PoliciesAreEffective(t *testing.T) {
	h, db, _ := newApprovalHandlerWithDB(t)

	metaRepo := testutil.NewMockMetaRepository()
	metaRepo.Meta = &model.Meta{
		ID:                        "x",
		ApprovalRequiredForSignup: true,
		Policies:                  []byte(`{"maxFileSizeMb": 20}`),
	}
	idGen, _ := id.NewGenerator("aidx")
	roleRepo := repository.NewRoleRepository(db)
	roleSvc := corerole.NewService(roleRepo, repository.NewRoleAssignmentRepository(db), metaRepo, idGen)
	h.SetUserPolicyResolver(roleSvc)

	// 条件ロールを 1 つ置く。これが無いと、作成直後の利用者はロールを持たないので
	// GetUserPolicies("") と GetUserPolicies(userID) が同じ値になり、**user ID を
	// 渡していない実装でもテストが通ってしまう**。isLocal は新規ローカル
	// アカウントに一致するので、ID が実際に使われているかを分離できる。
	// ID は helper の cleanup 規約 (itapi 接頭辞) に合わせる。合わせないと
	// 行が残り、次回以降が重複キーで落ちる。
	// 前回が異常終了して行が残っていても落ちないように、作る前にも消す。
	// 残っていると重複キーで落ち、policy の regression に見える red が出る。
	db.Exec(`DELETE FROM "role" WHERE id LIKE 'itapi%'`)
	t.Cleanup(func() { db.Exec(`DELETE FROM "role" WHERE id LIKE 'itapi%'`) })
	require.NoError(t, db.Create(&model.Role{
		ID:          "itapi-cond-local",
		Name:        "local",
		Target:      model.RoleTargetConditional,
		CondFormula: datatypes.JSON([]byte(`{"type":"isLocal"}`)),
		Policies:    datatypes.JSON([]byte(`{"pinLimit":{"useDefault":false,"priority":0,"value":99}}`)),
	}).Error)
	roleSvc.SetUserRepo(repository.NewUserRepository(db))

	rec := doPost(h.ApplicationRegister,
		`{"claimCode":"itapi-code","username":"itapieff","password":"hunter22"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	resp := parseResp(t, rec)
	policies, _ := resp["policies"].(map[string]any)
	assert.EqualValues(t, 20, policies["maxFileSizeMb"],
		"register 経路が meta の base override を反映していない")
	assert.EqualValues(t, 99, policies["pinLimit"],
		"register 経路が user ID を渡していない (条件ロールが効いていない)")
}
