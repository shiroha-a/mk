// #823 Phase 2 notification spec: reaction notification の round-trip。
//
// upstream Misskey TS と mk-go は両方とも:
//   - 他 user が自分の note に reaction を付ける → /api/i/notifications
//     で `type: 'reaction'` の notification を返す
//   - WS streaming main channel を subscribe していると同 notification
//     event が `{type: 'channel', body: {type: 'notification', body: <n>}}`
//     で push される
//
// 本 spec は両 backend 共通で:
//   1. user A + user B signup
//   2. user A が main channel を WS subscribe
//   3. user A が public note 投稿
//   4. user B がその note に reaction 付与
//   5. WS で notification event (type: 'reaction') 受信
//   6. /api/i/notifications で同 notification を取得 (= persistent)
//
// を検証する。streaming spec (#815) の WebSocket setup pattern を流用。
// 他 notification 種別 (mention / follow / chatMessage 等) は別 spec で
// 個別に拡張する想定。

import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { randomUsername, signupUser } from '../../fixtures/auth';
import { createNote } from '../../fixtures/notes';
import { resetRateLimit } from '../../fixtures/rate_limit';
import { awaitChannelEvent, openStream, subscribeChannel } from '../../fixtures/streaming';

test.describe('notifications: reaction', () => {
  test.beforeAll(() => {
    resetRateLimit();
  });

  test('reaction on my note triggers notification (WS push + /i/notifications)', async ({ request }) => {
    const me = await signupUser(request, randomUsername('ntfA'));
    const reactor = await signupUser(request, randomUsername('ntfB'));

    // me が main channel を subscribe。
    const ws = await openStream(me.token);
    const subId = subscribeChannel(ws, 'main');

    // notification event 受信用 Promise を投稿前に登録 (= race 回避)。
    type NotificationBody = { id: string; type: string; userId?: string; reaction?: string };
    const notifEventPromise = awaitChannelEvent<NotificationBody>(
      ws,
      (env) =>
        env.id === subId && env.type === 'notification' && env.body?.type === 'reaction',
    );

    // subscribe 確定までの短時間バッファ (streaming spec と同根拠)。
    await new Promise((resolve) => setTimeout(resolve, 200));

    // me が public note 投稿。
    const note = await createNote(request, me.token, {
      text: 'react to me',
      visibility: 'public',
    });

    // reactor が note に reaction 付与。upstream / mk-go は upstream 互換の
    // shorthand 'like' を含む custom reaction を accept するが、本 spec は
    // 標準 unicode emoji で deterministic に。
    const reactResp = await callApi(request, 'notes/reactions/create', {
      i: reactor.token,
      noteId: note.id,
      reaction: '👍',
    });
    expect(reactResp.status()).toBeGreaterThanOrEqual(200);
    expect(reactResp.status()).toBeLessThan(300);

    // WS notification event を待つ。
    const evNotif = await notifEventPromise;
    expect(evNotif.type).toBe('reaction');
    expect(evNotif.userId).toBe(reactor.id);

    ws.close();

    // /api/i/notifications でも同 notification が取得できる (= 永続化)。
    // notification の登録は async (= queue 経由) なので polling で待つ。
    let httpNotif: NotificationBody | undefined;
    await expect
      .poll(
        async () => {
          const resp = await callApi(request, 'i/notifications', { i: me.token });
          if (resp.status() !== 200) return false;
          const list = (await resp.json()) as NotificationBody[];
          httpNotif = list.find((n) => n.type === 'reaction' && n.userId === reactor.id);
          return httpNotif !== undefined;
        },
        { timeout: 5000, intervals: [100, 200, 500, 1000] },
      )
      .toBe(true);

    // 取得した notification が WS event と整合する shape を持つ。
    expect(httpNotif).toBeDefined();
    expect(httpNotif!.type).toBe('reaction');
    expect(httpNotif!.userId).toBe(reactor.id);
  });
});
