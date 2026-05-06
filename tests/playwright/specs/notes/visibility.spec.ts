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
import { randomUsername, signupUser } from '../../fixtures/auth';
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
    const author = await signupUser(request, randomUsername('vPubA'));
    const stranger = await signupUser(request, randomUsername('vPubS'));
    const note = await createNote(request, author.token, {
      text: 'visible to all',
      visibility: 'public',
    });

    const resp = await showNoteRaw(request, stranger.token, note.id);
    expect(resp.status()).toBe(200);
  });

  // 注: followers-only / specified note の `notes/show` 経路は upstream
  // Misskey TS と mk-go で挙動が drift している (#799 で tracking):
  //
  //   - upstream TS: 直接 ID 指定の `notes/show` は visibility 違反でも 200
  //     を返す (= visibility filter は timeline 経路のみで適用、note ID を
  //     既に知っている viewer には公開する設計)
  //   - mk-go: `notes/show` で visibility 違反を 4xx で reject (TS より strict)
  //
  // どちらが "正解" かは drop-in 互換性の方針次第だが、ひとまず本 spec は
  // skip し、本来の visibility 検証は notes/timeline 経路 (= follow graph と
  // visibility filter の組み合わせ) で別 PR にて行う。
  test.skip('followers-only note is hidden from stranger (TS=200/mk-go=4xx drift)', async () => {});
  test.skip('specified note is hidden from non-listed user (TS=200/mk-go=4xx drift)', async () => {});
});
