// #744 Phase 1 PR-3: visibility の境界。
// public / home / followers / specified の各 visibility で note を作成し、
// 第三者 viewer (= author 非関連) が notes/show で取得できるか / できないか
// を確認する。
//
// upstream Misskey TS は:
//   - public: 誰でも閲覧可
//   - followers: follower 限定。stranger は 4xx
//   - specified: visibleUserIds 列挙された user 限定。stranger は 4xx
//
// 本 spec は author + stranger 2 user で「stranger が見えるべきか」を
// 端的にチェックする。federation や follow を必要とせず mk-go single
// instance で実行可能。
//
// `home` visibility は follow graph の挙動 (follower で見える / 第三者は
// 不可視) が要るので別 spec で扱う想定。本 PR scope 外。

import { expect, test } from '@playwright/test';
import { signupUser } from '../../fixtures/auth';
import { createNote, showNoteRaw } from '../../fixtures/notes';
import { resetRateLimit } from '../../fixtures/rate_limit';

test.describe('notes: visibility boundary', () => {
  // 各 test で author + stranger を作るので spec ファイル全体で signup を
  // 6 回叩く。signup endpoint の 1h 5 回 limit を超えるので beforeAll では
  // なく beforeEach で毎 test 前に reset する。
  test.beforeEach(() => {
    resetRateLimit();
  });

  test('public note is readable by stranger', async ({ request }) => {
    const stamp = Date.now();
    const author = await signupUser(request, `vis_pub_a_${stamp}`);
    const stranger = await signupUser(request, `vis_pub_s_${stamp}`);
    const note = await createNote(request, author.token, {
      text: 'visible to all',
      visibility: 'public',
    });

    const resp = await showNoteRaw(request, stranger.token, note.id);
    expect(resp.status()).toBe(200);
  });

  test('followers-only note is hidden from stranger', async ({ request }) => {
    const stamp = Date.now();
    const author = await signupUser(request, `vis_fo_a_${stamp}`);
    const stranger = await signupUser(request, `vis_fo_s_${stamp}`);
    const note = await createNote(request, author.token, {
      text: 'followers only',
      visibility: 'followers',
    });

    // stranger は author をフォローしていないので閲覧不可 → 4xx。
    const resp = await showNoteRaw(request, stranger.token, note.id);
    expect(resp.status()).toBeGreaterThanOrEqual(400);
    expect(resp.status()).toBeLessThan(500);
  });

  test('specified note is hidden from non-listed user', async ({ request }) => {
    const stamp = Date.now();
    const author = await signupUser(request, `vis_sp_a_${stamp}`);
    const stranger = await signupUser(request, `vis_sp_s_${stamp}`);
    const note = await createNote(request, author.token, {
      text: 'private DM',
      visibility: 'specified',
      visibleUserIds: [author.id], // stranger は含まない
    });

    const resp = await showNoteRaw(request, stranger.token, note.id);
    expect(resp.status()).toBeGreaterThanOrEqual(400);
    expect(resp.status()).toBeLessThan(500);
  });
});
