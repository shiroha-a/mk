// #823 Phase 2 notification spec: mention notification の round-trip。
//
// upstream Misskey TS と mk-go は両方とも:
//   - 他 user の note 本文に `@username` が含まれる → mention された user
//     の /api/i/notifications に `type: 'mention'` の notification が
//     登録される
//   - WS streaming main channel を subscribe していると同 notification
//     event が push される
//
// 本 spec は両 backend 共通で:
//   1. user A + user B signup
//   2. user A が main channel を WS subscribe
//   3. user B が `@<A.username>` を含む public note を投稿
//   4. WS で notification event (type: 'mention') 受信
//   5. /api/i/notifications で同 notification を取得 (= persistent)
//
// reaction.spec.ts と同 pattern。fixtures/streaming.ts の helper 群を流用。

import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { randomUsername, signupUser } from '../../fixtures/auth';
import { createNote } from '../../fixtures/notes';
import { resetRateLimit } from '../../fixtures/rate_limit';
import { awaitChannelEvent, openStream, subscribeChannel } from '../../fixtures/streaming';

test.describe('notifications: mention', () => {
  test.beforeAll(() => {
    resetRateLimit();
  });

  test('mention in another user note triggers notification', async ({ request }) => {
    const me = await signupUser(request, randomUsername('mntA'));
    const sender = await signupUser(request, randomUsername('mntB'));

    const ws = await openStream(me.token);
    const subId = subscribeChannel(ws, 'main');

    type NotificationBody = { id: string; type: string; userId?: string };
    const notifEventPromise = awaitChannelEvent<NotificationBody>(
      ws,
      (env) =>
        env.id === subId && env.type === 'notification' && env.body?.type === 'mention',
    );

    await new Promise((resolve) => setTimeout(resolve, 200));

    // sender が me を mention する note を投稿。
    await createNote(request, sender.token, {
      text: `hi @${me.username}`,
      visibility: 'public',
    });

    const evNotif = await notifEventPromise;
    expect(evNotif.type).toBe('mention');
    expect(evNotif.userId).toBe(sender.id);

    ws.close();

    // /api/i/notifications でも同 notification が取得できる (= 永続化)。
    let httpNotif: NotificationBody | undefined;
    await expect
      .poll(
        async () => {
          const resp = await callApi(request, 'i/notifications', { i: me.token });
          if (resp.status() !== 200) return false;
          const list = (await resp.json()) as NotificationBody[];
          httpNotif = list.find((n) => n.type === 'mention' && n.userId === sender.id);
          return httpNotif !== undefined;
        },
        { timeout: 5000, intervals: [100, 200, 500, 1000] },
      )
      .toBe(true);

    expect(httpNotif).toBeDefined();
    expect(httpNotif!.type).toBe('mention');
    expect(httpNotif!.userId).toBe(sender.id);
  });
});
