// #821 PR-C reactions spec: 違う reaction を 2 度目に送ったときの挙動。
//
// PR-B (#863) で同じ reaction を 2 度送ると ALREADY_REACTED で reject される
// ことを spec 化した。本 spec はその延長として、違う reaction (例: `👍` の
// 後に `❤️`) を送った時の挙動を両 backend で観察し共通仕様を確認した:
//
//   - 両 backend ともに 204 で受け入れ (= 置き換え挙動)
//   - 1 個目の reaction (`👍`) は unset され、2 個目 (heart) のみ残る
//   - reactions object は 1 key / count=1 になる
//
// 同時に検出した drift (#864 で fix 予定): mk-go は `❤️` (variation selector
// `\ufe0f` 付き) を保存、upstream TS は `❤` に normalize する。本 spec は
// `startsWith('❤')` で variation selector の有無を吸収して「置き換え」挙動
// 自体を strict 確認する。#864 fix 完了後は strict 文字列比較 (`'❤'` 固定)
// に置き換える follow-up を予定。

import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { randomUsername, signupUser } from '../../fixtures/auth';
import { createNote } from '../../fixtures/notes';
import { resetRateLimit } from '../../fixtures/rate_limit';

test.describe('reactions: different-reaction replay (replaced, not duplicated)', () => {
  test.beforeAll(() => {
    resetRateLimit();
  });

  test('B reacts with thumbs-up then heart; first is unset, only heart remains', async ({
    request,
  }) => {
    const author = await signupUser(request, randomUsername('rdA'));
    const reactor = await signupUser(request, randomUsername('rdB'));

    const note = await createNote(request, author.token, {
      text: 'react diff',
      visibility: 'public',
    });

    // 1 個目: `👍` 付与 → 204。
    const first = await callApi(request, 'notes/reactions/create', {
      i: reactor.token,
      noteId: note.id,
      reaction: '👍',
    });
    expect(first.status()).toBeGreaterThanOrEqual(200);
    expect(first.status()).toBeLessThan(300);

    // 2 個目: 違う `❤️` を送る → 両 backend で 204 で受け入れ (置き換え)。
    const second = await callApi(request, 'notes/reactions/create', {
      i: reactor.token,
      noteId: note.id,
      reaction: '❤️',
    });
    expect(second.status()).toBeGreaterThanOrEqual(200);
    expect(second.status()).toBeLessThan(300);

    // 最終 state: 1 個目の `👍` は unset、2 個目の heart のみ残る。
    const showResp = await callApi(request, 'notes/show', {
      i: author.token,
      noteId: note.id,
    });
    expect(showResp.status()).toBe(200);
    const shown = (await showResp.json()) as { reactions: Record<string, number> };
    // 1 reactor / 1 reaction なので reactions object は 1 key のみ。
    expect(Object.keys(shown.reactions)).toHaveLength(1);
    // 1 個目の reaction (`👍`) は置き換えで unset されているので存在しない。
    expect(shown.reactions['👍']).toBeUndefined();
    // 2 個目の heart は variation selector の drift がある (mk-go: `❤️`,
    // TS: `❤`)。emoji 正規化の drift fix は別 issue で扱う scope のため、
    // 本 spec では heart の prefix で吸収して count = 1 を strict 確認。
    const newKey = Object.keys(shown.reactions)[0];
    expect(newKey.startsWith('❤')).toBe(true);
    expect(shown.reactions[newKey]).toBe(1);
  });
});
