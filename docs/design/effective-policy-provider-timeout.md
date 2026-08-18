# Effective-policy provider timeout

## 目的

effective-policy resolverがI/O待ちや実装不備で返らなくても、通常のAPI requestが無期限に停止しないようにする。同時に、contextを無視するresolverをhostから強制終了できないというtrusted in-process pluginの制約を維持し、timeoutのたびにgoroutineを増やさない。

## 契約

- provider解決の期限は1秒とする。設定項目は追加しない。
- 期限はproviderの実行token取得とresolver完了を合わせた全体に適用する。
- timeoutしたproviderはprocess再起動まで無効化する。
- disabled providerはresolverを再実行せず、既存のprovider failure経路へ合流する。
- failed providerが宣言したkeyはplugin適用前のexact native policyへ戻し、同じkeyへの全plugin contributionを破棄する。
- resolverへは同じdeadlineを持つcontextを渡す。plugin.Storageなどcontext対応I/Oは協調的に停止できる。
- timeout、panic、errorにはplugin名、user/role/policy ID、provider output、panic値、内部errorを含めない。

## 実行制御

provider runtimeは次の状態を持つ。

- capacity 1の実行token
- process lifetimeのatomic disabled flag

解決手順は次のとおり。

1. disabledなら直ちにfailureを返す。
2. 1秒のdeadline contextを作る。
3. token取得を待つ。deadlineまでに取得できなければproviderをdisableし、failureを返す。
4. token取得後にdisabledを再確認する。別の呼び出しが待機中にdisableしていた場合はtokenを返し、resolverを開始せずfailureを返す。
5. resolverを専用goroutineで実行し、buffered result channelへ結果を送る。
6. resolver完了ならtokenを返し、既存validationへ結果を渡す。
7. deadlineが先ならproviderをdisableし、failureを返す。resolver goroutineは強制終了しない。

tokenはresolver goroutineがreturnしたときだけ返す。contextを無視して永久にhangするresolverでも、同じproviderで残留するgoroutineは最大1本になる。timeout後は実行前とtoken取得後のdisabled checkで新しいgoroutineを作らない。

deadline直前の完了とtimeoutが競合した場合、どちらをselectしても安全側の結果になる。timeoutを選んだ場合はproviderをdisableしてnative fallback、resultを選んだ場合は通常validationを行う。

## Registry invariant

同じplugin名のproviderは重複登録を拒否する。これによりplugin名順registryの一意性とdeterministic aggregationを保証する。登録済みproviderの置換やruntime再有効化は提供しない。

## Arithmetic invariant

internal/safemath.MulFloat64はNaNを0へ正規化する。有限値の通常乗算、fractional policy、小数切り捨て、正負overflow時の飽和は既存契約を維持する。

## テスト

- deadline contextを尊重するresolverがtimeoutし、native fallbackになる。
- contextを無視するresolverでも呼び出し元が1秒付近で戻る。
- concurrent resolutionでresolver実行数と残留goroutineがproviderごと最大1になる。
- timeout後の呼び出しがresolverを再実行しない。
- timeoutしたproviderのdeclared keyだけが既存fallback規則へ合流する。
- duplicate provider nameを登録時に拒否する。
- MulFloat64(NaN, unit)が0を返す。
- panic、error、invalid output、normal contributionの既存testを維持する。

## 対象外

- resolver goroutineのhard preemption
- timeout後の自動retryや一時circuit breaker
- runtimeでのprovider再有効化
- timeout値のoperator設定
- provider output cache
