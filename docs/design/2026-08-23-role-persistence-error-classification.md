# Role persistence error分類設計

Issue: #2625

## 目的

roleが存在しない場合と、DB接続障害などrole lookup自体が失敗した場合をserviceとAPIの両層で区別する。role不在の既存HTTP 400 `NO_SUCH_ROLE`契約は維持し、persistence errorはraw errorやidentifierをclientへ露出せず、共通HTTP 500 `INTERNAL_ERROR`へ変換する。

## 根本原因

問題は2層にある。

1. `role.Service`の`Assign`、`ListByRole`、`UpdateFields`、`Delete`が`RoleRepository.FindByID`の全errorを`ErrRoleNotFound`へ変換している。`FindRole`は逆にrepositoryのnot-foundをsentinelへ変換していない。
2. public/admin handlerの一部が`ErrRoleNotFound`とその他のerrorを区別せず、全てendpoint固有のHTTP 400 `NO_SUCH_ROLE`へ変換している。

`Service.Show`、public/adminのassignment-show、`requireCanEditRoleMembers`は既に正しく分類している。

## Service境界

`internal/core/role/role_service.go`へ非公開`findRoleByID(id string) (*model.Role, error)`を追加する。

- 空IDは`ErrRoleNotFound`。
- `RoleRepository.FindByID`が`gorm.ErrRecordNotFound`またはそれをwrapしたerrorを返した場合だけ`ErrRoleNotFound`。
- その他のrepository errorは元errorを保持して返す。
- 正常時は取得したroleを返す。

次の全存在確認をこのhelperへ統一する。

- `Assign`
- `Show`
- `ListByRole`
- `UpdateFields`の事前確認、空fields時のreadback、更新後readback
- `Delete`
- `FindRole`

これにより公開service契約を「role不在だけsentinel、その他のpersistence errorは透過」に揃える。

## Handler分類

対象handlerはservice errorを次の2系統に分類する。

- `errors.Is(err, role.ErrRoleNotFound)`: 既存のHTTP 400、`NO_SUCH_ROLE`、message、endpoint固有UUIDを返す。
- その他: `apierr.JSONInternalError(c)`でHTTP 500、共通`INTERNAL_ERROR`を返す。

### Public endpoints

| Endpoint | Service call | `NO_SUCH_ROLE` UUID |
|---|---|---|
| `/api/roles/show` | `Show` | `de5502bf-009a-4639-86c1-fec349e46dcb` |
| `/api/roles/users` | `Show`、`ListByRole` | `30aaaee3-4792-48dc-ab0d-cf501a575ac5` |
| `/api/roles/notes` | `Show` | `eb70323a-df61-4dd4-ad90-89c83c7cf26e` |

`roles/users`は最初の`Show`成功後に`ListByRole`の存在確認がnot-foundになった場合も、同endpointのUUIDで400を返す。それ以外の`ListByRole` errorは500にする。

### Admin endpoints

| Endpoint | Service call | `NO_SUCH_ROLE` UUID |
|---|---|---|
| `/api/admin/roles/show` | `Show` | `07dc7d34-c0d8-49b7-96c6-db3ce64ee0b3` |
| `/api/admin/roles/update` | `Show`、`UpdateFields` | `cd23ef55-09ad-428a-ac61-95a45e124b32` |
| `/api/admin/roles/delete` | `Delete` | `de0d6ecd-8e0a-4253-88ff-74bc89ae3d45` |
| `/api/admin/roles/assign` | `Assign` | `6503c040-6af4-4ed9-bf07-f2dd16678eab` |
| `/api/admin/roles/users` | `ListByRole` | `224eff5e-2488-4b18-b3e7-f50d94421648` |

`admin/roles/update`は更新前snapshotの`Show`と`UpdateFields`内部lookupを別々に分類する。`admin/roles/assign`と`admin/roles/users`に残る直接比較も`errors.Is`へ統一する。

## Client error契約

role不在時は各endpointの既存shapeを変えない。

```json
{
  "error": {
    "message": "No such role.",
    "code": "NO_SUCH_ROLE",
    "id": "<endpoint固有UUID>",
    "kind": "client"
  }
}
```

persistence error時は共通shapeを返す。

```json
{
  "error": {
    "message": "Internal error.",
    "code": "INTERNAL_ERROR",
    "id": "5d37dbcb-891e-41ca-a3d6-e690c97775ac",
    "kind": "server"
  }
}
```

raw repository error、SQL、role ID、その他のidentifierをresponseへ含めない。本issueでは新しいhandler logを追加しない。

## Intentional best-effort

次の処理はlookup失敗を本操作の失敗へ昇格させない。

- `RolesDelete`のmoderation log用削除前snapshot
- `modlog_helpers.go`のrole名補助lookup
- account moveの`copyRoles`から呼ぶ`FindRole`
- `Assign` / `Unassign`後の`lastUsedAt`更新
- pack用の`CountAssignedUsers`

`findRoleByID`により`FindRole`のerror分類自体は揃うが、account move側は従来どおりerrorをbest-effortでskipする。

## 既に正しい経路

次は変更しない。

- public `/api/roles/assignment-show`
- admin `/api/admin/roles/assignment-show`
- `requireCanEditRoleMembers`
- `Unassign`のassignment存在確認

## 対象外

- `UpdateFields` / `Delete`の事前lookup後に行が消える競合
- repository `RowsAffected == 0`のdomain error化
- transaction境界の変更
- repository interface変更
- migration、schema、upstream submodule、`CHANGELOG.md`
- 共通error helperや共通logging基盤の追加

## Test戦略

実装はTDDで行う。

### Service tests

`RoleRepository`を埋め込み、`FindByID`だけ任意errorを返すtest wrapperを使う。production mockへ汎用error injection fieldは追加しない。

次を固定する。

- `Assign`、`Show`、`ListByRole`、`UpdateFields`、`Delete`: wrapped `gorm.ErrRecordNotFound`は`ErrRoleNotFound`、generic errorは元errorへ`errors.Is`でき、`ErrRoleNotFound`ではない。
- `UpdateFields`: 事前確認、空fields readback、更新後readbackの各lookup error。
- `FindRole`: 空IDとwrapped not-foundは`ErrRoleNotFound`、generic errorは透過。
- assignment repository、更新本体、削除本体のerror契約は既存testを維持する。

### Handler tests

public/adminそれぞれ既存のassignment-show test patternを使い、repository failureをhandlerまで到達させる。

- 各対象endpointのrole不在はHTTP 400、`NO_SUCH_ROLE`、既存固有UUID。
- generic persistence errorはHTTP 500、exact共通internal error body。
- 500 bodyはraw error文字列とrole IDを含まない。
- `roles/users`は`Show`成功後の`ListByRole` lookup failureをsequence repositoryで検証する。
- `admin/roles/update`は事前`Show` failureと`UpdateFields`内部lookup failureを分ける。
- `admin/roles/delete`は存在確認lookup failureと、存在確認成功後のdelete本体errorを分け、どちらも500になることを確認する。
- `admin/roles/assign`は認可用`Show`成功後、`Assign`内部の2回目lookupを失敗させるsequence repositoryで500を確認する。
- `admin/roles/users`はnot-foundとgeneric lookup failureを分ける。
- best-effortの2 handler lookupはfailureでも本操作を阻害しない。

### Mutation checks

次の一時変更で対応testがredになることを確認する。

- `findRoleByID`が全repository errorを`ErrRoleNotFound`へ変換する。
- public handlerが全errorを`NO_SUCH_ROLE`へ変換する。
- admin handlerが全errorを`NO_SUCH_ROLE`へ変換する。
- `roles/users`の`ListByRole` not-foundを500へ戻す。

## Verification

fresh PostgreSQL 18を使い、次を確認する。

- `internal/core/role`、`internal/api/roles`、`internal/api/admin`のLinux race/atomic coverage
- repository-wide Linux buildとvet
- repository-wide `gofmt -s -d`
- repository gates
- 各implementation commitのLinux build
- `git diff --check`

検証後はcontainer、network、volume、coverage artifactを撤去する。

## Commit構成

各commitを単体build可能にする。

1. service lookup分類helperとservice tests
2. public role handler分類とtests
3. admin role handler分類とtests
