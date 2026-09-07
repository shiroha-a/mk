/*
 * SPDX-FileCopyrightText: mk-go project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

// /about-mkgo が AGPL-3.0 section 13 の案内として成立していることを実ブラウザで
// 見る (#2700)。
//
// **`mkgo/` に置く。** `/about-mkgo` は fork frontend にしか無いので、公式 image
// を使う `playwright-ts-test` では必ず 404 になる。
//
// Go 側のテストは `EnsureInitial` が repositoryUrl を入れることと meta が
// providesTarball を false で返すことまでしか見ない。**新規インスタンスを起動
// した結果としてページに案内が出るか**は、DB 作成から通しでやらないと分からない。
// playwright スタックは clean DB から migration を流して起動する (`make
// playwright-check` / CI は `down -v` を挟む) ので、この spec が「新規インスタンスで
// 案内が出ない」状態への回帰を検出する経路になる。**volume を残したまま
// `playwright-up` だけを叩いた場合は既存 meta 行が残り、`migration/000084` の
// backfill が代わりに埋めるので `EnsureInitial` 側の回帰は捕まらない。**
//
// **検査していない導線が 2 つある。** サイドバーのインスタンスメニュー
// (`ui/_common_/common.ts` の `openInstanceMenu`) はボタンを押すまで DOM に無く、
// `MkSourceCodeAvailablePopup` は「サインイン済み + localStorage 未設定」でしか
// 出ない (`boot/main-boot.ts`)。どちらもこの spec の未認証セッションからは
// 到達できないので、回帰はレビューで見るしかない。

import { expect, test } from '@playwright/test';
import { callApi } from '../../../fixtures/api';

const MKGO_REPOSITORY_URL = 'https://github.com/shiroha-a/mk';
const MKGO_FRONTEND_REPOSITORY_URL = 'https://github.com/shiroha-a/misskey-ts';

test.describe('UI: /about-mkgo', () => {
  test.setTimeout(60_000);

  // ページの前提。meta が空だとページ側はリンクを 1 本落とすだけなので、
  // 「案内が出た」ことの意味が変わる。先に固定する。
  test('a fresh instance advertises its source and ignores the tarball flag', async ({ request }) => {
    const resp = await callApi(request, 'meta', { detail: false });
    expect(resp.status()).toBe(200);
    const meta = await resp.json();

    expect(meta.repositoryUrl, 'repositoryUrl が空。EnsureInitial の既定値が効いていない').toBeTruthy();
    // **instance.yml は publishTarballInsteadOfProvideRepositoryUrl: true を
    // 明示している。** mk-go に /tarball/ のルートは無く、SPA catchall が HTML を
    // 200 で返してしまうので、設定が通ると壊れた tarball を配ることになる。
    // ここが true になったら `config.ProvidesTarball()` が設定値を返す形に
    // 戻っている。
    expect(meta.providesTarball, 'providesTarball が true。mk-go に /tarball/ は無い').toBe(false);
  });

  test('renders the mk-go page with source, license and Misskey attribution', async ({ page, baseURL }) => {
    const resp = await page.goto(`${baseURL}/about-mkgo`, { waitUntil: 'domcontentloaded' });
    expect(resp!.status()).toBe(200);

    // **このサーバーが動かしているコードの案内。** repositoryUrl が未設定
    // (NULL / 空 / upstream の列 DEFAULT のまま) だとページ側が落とすので、
    // EnsureInitial と migration 000084 の回帰はここで落ちる。mk-go 本体への
    // リンクとは href が一致しうるため、`data-testid` で区別する。
    const serverSource = page.locator('[data-testid="about-mkgo-server-source"] a');
    await expect(serverSource).toBeVisible({ timeout: 20_000 });
    await expect(serverSource).toHaveAttribute('href', /^https?:\/\//);

    // **フロントエンドのソース。** Go で書き直したのはサーバーサイドだけで、
    // いま表示されている画面は別リポジトリにある。これが消えると「動いている
    // コード」の案内が片側だけになる。
    await expect(page.locator(`a[href="${MKGO_FRONTEND_REPOSITORY_URL}"]`)).toBeVisible();

    // ライセンスの提示。AGPL であることが分かる導線が消えたら落とす。
    await expect(page.locator(`a[href="${MKGO_REPOSITORY_URL}/blob/develop/LICENSE"]`)).toBeVisible();

    // Misskey への帰属。ページから /about-misskey へ辿れること。
    await expect(page.locator('a[href="/about-misskey"]')).toBeVisible();
  });

  // **tarball リンクを見るのは /about-misskey と /about。** `about-mkgo.vue` は
  // tarball の分岐を持たないので、そこで数えても常に 0 で空振りする。この 2 つが
  // `instance.providesTarball` を読む唯一の場所 (fork 全体を grep して確認)。
  test('no tarball link is rendered even though the config enables it', async ({ page, baseURL }) => {
    await page.goto(`${baseURL}/about-misskey`, { waitUntil: 'domcontentloaded' });
    // ページの mount を待ってから数える (mount 前は何も無いので必ず 0 になる)。
    await expect(page.locator('a[href="/about-mkgo"]')).toBeVisible({ timeout: 20_000 });
    await expect(page.locator('a[href^="/tarball/"]')).toHaveCount(0);

    await page.goto(`${baseURL}/about`, { waitUntil: 'domcontentloaded' });
    await expect(page.locator('a[href="/about-mkgo"]').first()).toBeVisible({ timeout: 20_000 });
    await expect(page.locator('a[href^="/tarball/"]')).toHaveCount(0);
  });

  test('navigates from /about-mkgo to /about-misskey and back', async ({ page, baseURL }) => {
    await page.goto(`${baseURL}/about-mkgo`, { waitUntil: 'domcontentloaded' });
    await page.locator('a[href="/about-misskey"]').first().click();
    await page.waitForURL('**/about-misskey', { timeout: 20_000 });

    // 逆向きの導線。upstream のページからも mk-go 側へ戻れること。
    await page.locator('a[href="/about-mkgo"]').first().click();
    await page.waitForURL('**/about-mkgo', { timeout: 20_000 });
  });
});
