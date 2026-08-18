# Effective-policy provider timeout

## 目的

effective-policy resolverがI/O待ちや実装不備で返らなくても、通常のAPI requestが無期限に停止しないようにする。同時に、contextを無視するresolverをhostから強制終了できないというtrusted in-process pluginの制約を維持し、timeoutのたびにgoroutineを増やさない。

## 契約

- providerの実行token取得待ちとresolver実行には、それぞれ独立した1秒の期限を適用する。設定項目は追加しない。
- tokenを1秒以内に取得できなかった場合は、そのrequestだけを既存のprovider failure経路へ合流させる。providerは無効化しない。
- token取得後にresolver専用の新しい1秒deadline contextを作る。resolver自身が期限内に完了しなかった場合だけ、providerをprocess再起動まで無効化する。
- token待ちとresolver実行を両方使い切ったrequestの最悪応答時間は約2秒になる。
- disabled providerはresolverを再実行せず、既存のprovider failure経路へ合流する。
- failed providerが宣言したkeyはplugin適用前のexact native policyへ戻し、同じkeyへの全plugin contributionを破棄する。
- resolverへは実行専用deadlineを持つcontextを渡す。plugin.Storageなどcontext対応I/Oは協調的に停止できる。
- timeout、panic、errorにはplugin名、user/role/policy ID、provider output、panic値、内部errorを含めない。

## 実行制御

provider runtimeは次の状態を持つ。

- capacity 1の実行token
- process lifetimeのatomic disabled flag

解決手順は次のとおり。

1. disabledなら直ちにfailureを返す。
2. token取得待ち専用の1秒deadline contextを作る。
3. token取得を待つ。deadlineまでに取得できなければproviderをdisableせず、そのrequestだけfailureを返す。
4. token取得後にdisabledを再確認する。別の呼び出しが待機中にdisableしていた場合はtokenを返し、resolverを開始せずfailureを返す。
5. token待ちcontextを破棄し、resolver実行専用の新しい1秒deadline contextを作る。
6. resolverを専用goroutineで実行し、完了時刻を記録してbuffered result channelへ結果を送る。完了時刻が実行deadline以後なら、tokenを返す前にdisabled flagを設定する。
7. resolver完了ならtokenを返し、既存validationへ結果を渡す。
8. resolver実行deadlineが先ならproviderをdisableし、failureを返す。resolver goroutineは強制終了しない。

tokenはresolver goroutineがreturnしたときだけ返す。contextを無視して永久にhangするresolverでも、同じproviderで残留するgoroutineは最大1本になる。timeout後は実行前とtoken取得後のdisabled checkで新しいgoroutineを作らない。deadline以後にresolverがreturnした場合はresolver goroutine自身がtoken返却前にdisableするため、timeoutを処理するcallerとのschedule順にかかわらず、待機callerが新しいresolverを開始できない。

resolver実行deadline直前の完了とtimeoutが競合した場合は、resolver goroutineが記録した完了時刻とdeadlineを比較する。deadline前に完了したresultだけを通常validationへ渡し、deadline以後のresultはproviderをdisableしてnative fallbackへ戻す。

token取得待ちのtimeoutはproviderの健全性を示さない。同じruntimeを共有する正常なresolverがburstを直列処理しているだけでも発生するため、permanent disabled flagを変更してはならない。このrequestではpolicyがnative fallbackへ戻るが、後続requestはtoken取得とresolver実行を再試行できる。

## Provider未登録時のfast path

providerを登録していないinstanceでは、plugin機構追加前のnative policy解決経路と計算量を維持する。

- provider snapshotはnative policyの全key集約やexact native snapshot作成より前に取得する。
- provider未登録時のdefault policy copyは従来のshallow map cloneを使う。pluginへslice値を渡さないため、provider経路用のdeep cloneは不要とする。
- anonymous requestはmeta base policy適用後、全keyの`computePolicy`を行わずserver/instance capを適用して返す。
- userにactive roleが無い場合も同じ早期returnを使う。
- active roleがありprovider未登録の場合は従来どおりnative role aggregationを行うが、provider failure用のexact native deep cloneは作らない。
- provider登録時だけdeep-cloned base、全keyのnative aggregation、failure fallback用snapshotを作成する。

この分岐はproviderの有無だけで決まり、provider登録後に出力cacheや別のpolicy semanticsを導入しない。

## Registry invariant

同じplugin名のproviderは重複登録を拒否する。これによりplugin名順registryの一意性とdeterministic aggregationを保証する。登録済みproviderの置換やruntime再有効化は提供しない。

## Arithmetic invariant

internal/safemath.MulFloat64はNaNを0へ正規化する。有限値の通常乗算、fractional policy、小数切り捨て、正負overflow時の飽和は既存契約を維持する。

## テスト

- deadline contextを尊重するresolverがtimeoutし、native fallbackになる。
- contextを無視するresolverでも呼び出し元が1秒付近で戻る。
- concurrent resolutionでresolver実行数と残留goroutineがproviderごと最大1になる。
- timeout後の呼び出しがresolverを再実行しない。
- cooperative resolverがdeadline時にreturnしてtokenを返す競合でも、待機requestが新しいresolverを開始しない。
- 正常な短時間resolverへtoken capacityを超えるconcurrent requestを送っても、token待ちtimeoutだけではproviderがdisableされない。
- token待ちに1秒近く費やしても、token取得後のresolverには独立した1秒の実行期限が与えられる。
- timeoutしたproviderのdeclared keyだけが既存fallback規則へ合流する。
- provider未登録のanonymous、roleなし、roleあり経路が従来の早期returnとcopy範囲を維持する。
- provider未登録のanonymous経路をmerge-baseと同じ条件でbenchmarkし、全key集約とdeep cloneのcostが残っていないことを確認する。
- duplicate provider nameを登録時に拒否する。
- MulFloat64(NaN, unit)が0を返す。
- panic、error、invalid output、normal contributionの既存testを維持する。

## 対象外

- resolver goroutineのhard preemption
- resolver実行timeout後の自動retryや一時circuit breaker
- runtimeでのprovider再有効化
- timeout値のoperator設定
- provider output cache
- token待ちtimeout時のprovider disable
