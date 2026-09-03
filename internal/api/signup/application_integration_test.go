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

// 承認で内部発行する ticket は、審査した管理者の招待枠を消費しない (#2805)。
//
// `createdById` に審査した管理者を入れると、承認された申請から登録が行われるたびに
// 1 枚その管理者名義の招待が増え、`invite/create` / `invite/limit` の上限
// (`CountByCreatorSince`) を食い、利用者の `invite/list` (`ListByCreator`) にも出る。
// **効いている構成では承認するほど自分の招待枠が減る。** どちらの query も
// `WHERE "createdById" = ?` なので、NULL なら両方から外れる。
//
// **審査した管理者の user 行を必ず作る。** `registration_ticket.createdById` には
// FK があるので、実在しない ID を入れる形にすると変異させたときに ticket の作成
// 自体が FK 違反で落ち、**上限のアサーションまで到達しないまま緑/赤が決まる**
// (= quota の検証が空振りする)。
func TestApplicationRegister_ApprovalTicketDoesNotConsumeModeratorQuota(t *testing.T) {
	h, db, app := newApprovalHandlerWithDB(t)
	ticketRepo := repository.NewRegistrationTicketRepository(db)
	h.SetTicketStore(ticketRepo)

	const moderator = "itapi_mod1"
	// 前回が異常終了して行が残っていても落ちないように、作る前にも消す
	// (同ファイルの role fixture と同じ規約)。残っていると重複キーで落ち、
	// #2805 の regression に見える red が出る。
	db.Exec(`DELETE FROM "user" WHERE id = ?`, moderator)
	require.NoError(t, db.Create(&model.User{
		ID: moderator, Username: "itapimod1", UsernameLower: "itapimod1",
		AvatarDecorations: []byte("[]"),
	}).Error)
	require.NoError(t, db.Model(&model.SignupApplication{}).
		Where("id = ?", app.ID).Update("processedById", moderator).Error)

	// 正常系では base の cleanup で消える (`usedById` の FK が
	// ON DELETE CASCADE なので、申請者を消せば ticket も連鎖して消える)。
	// **ここは異常系の保険。** mint 済みで消費前に落ちると `usedById` が NULL の
	// まま誰にも紐付かず残り、次の実行の `preexisting` に入って永久に居座る。
	var preexisting []string
	require.NoError(t, db.Model(&model.RegistrationTicket{}).Pluck("id", &preexisting).Error)
	t.Cleanup(func() {
		if len(preexisting) == 0 {
			db.Exec(`DELETE FROM "registration_ticket"`)
			return
		}
		db.Exec(`DELETE FROM "registration_ticket" WHERE id NOT IN ?`, preexisting)
	})

	rec := doPost(h.ApplicationRegister,
		`{"claimCode":"itapi-code","username":"itapiq1","password":"hunter22"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// 上限カウントにも利用者の一覧にも出ない。sinceID は空文字なので
	// `"id" > ''` が全行に当たる = 期間を問わず 0 件であることを見ている。
	count, err := ticketRepo.CountByCreatorSince(moderator, "")
	require.NoError(t, err)
	assert.Equal(t, int64(0), count, "承認は審査した管理者の招待枠を消費しない")

	owned, err := ticketRepo.ListByCreator(moderator, "", "", 100)
	require.NoError(t, err)
	assert.Empty(t, owned, "利用者の invite/list に出ない")

	// **監査の連鎖は createdById 抜きで繋がる。** 申請から ticket を辿るので、
	// 残留行を拾って実装のバグに見せかけることも無い。
	var stored model.SignupApplication
	require.NoError(t, db.Where("id = ?", app.ID).First(&stored).Error)
	assert.Equal(t, model.SignupApplicationCompleted, stored.Status)
	require.NotNil(t, stored.ProcessedByID)
	assert.Equal(t, moderator, *stored.ProcessedByID, "審査した管理者は申請から辿る")
	require.NotNil(t, stored.TicketID, "ticket との対応は申請から辿る")

	minted, err := ticketRepo.FindByID(*stored.TicketID)
	require.NoError(t, err)
	assert.Nil(t, minted.CreatedByID, "管理者名義にしない")

	var user model.User
	require.NoError(t, db.Where(`"usernameLower" = ?`, "itapiq1").First(&user).Error)
	require.NotNil(t, minted.UsedByID)
	assert.Equal(t, user.ID, *minted.UsedByID, "登録者は ticket から辿る")

	// admin/invite/list は createdById で絞らないので従来どおり出る。
	rows, err := ticketRepo.ListSorted("", "", 100, 0, time.Now())
	require.NoError(t, err)
	found := false
	for _, r := range rows {
		if r.ID == minted.ID {
			found = true
		}
	}
	assert.True(t, found, "admin 側の一覧には出ること")
}
