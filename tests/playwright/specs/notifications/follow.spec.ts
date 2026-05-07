// #823 Phase 2 notification spec: follow notification の round-trip。
//
// upstream Misskey TS と mk-go は両方とも:
//   - user B が user A を follow する → A の /api/i/notifications に
//     `type: 'follow'` の notification が登録される
//   - WS streaming main channel を subscribe していると同 notification
//     event が push される
//
// 本 spec は両 backend 共通で:
//   1. user A + user B signup
//   2. user A が main channel を WS subscribe
//   3. user B が user A を follow (POST /api/following/create)
//   4. WS で notification event (type: 'follow') 受信
//   5. /api/i/notifications で同 notification を取得 (= persistent)
//
// reaction / mention spec と同 pattern。

import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { randomUsername, signupUser } from '../../fixtures/auth';
import { resetRateLimit } from '../../fixtures/rate_limit';
import { awaitChannelEvent, openStream, subscribeChannel } from '../../fixtures/streaming';

test.describe('notifications: follow', () => {
  test.beforeAll(() => {
    resetRateLimit();
  });

  test('being followed triggers notification', async ({ request }) => {
    const me = await signupUser(request, randomUsername('flwA'));
    const follower = await signupUser(request, randomUsername('flwB'));

    const ws = await openStream(me.token);
    const subId = subscribeChannel(ws, 'main');

    type NotificationBody = { id: string; type: string; userId?: string };
    const notifEventPromise = awaitChannelEvent<NotificationBody>(
      ws,
      (env) =>
        env.id === subId && env.type === 'notification' && env.body?.type === 'follow',
    );

    await new Promise((resolve) => setTimeout(resolve, 200));

    // follower が me を follow する。
    const followResp = await callApi(request, 'following/create', {
      i: follower.token,
      userId: me.id,
    });
    expect(followResp.status()).toBeGreaterThanOrEqual(200);
    expect(followResp.status()).toBeLessThan(300);

    const evNotif = await notifEventPromise;
    expect(evNotif.type).toBe('follow');
    expect(evNotif.userId).toBe(follower.id);

    ws.close();

    // /api/i/notifications でも同 notification が取得できる (= 永続化)。
    let httpNotif: NotificationBody | undefined;
    await expect
      .poll(
        async () => {
          const resp = await callApi(request, 'i/notifications', { i: me.token });
          if (resp.status() !== 200) return false;
          const list = (await resp.json()) as NotificationBody[];
          httpNotif = list.find((n) => n.type === 'follow' && n.userId === follower.id);
          return httpNotif !== undefined;
        },
        { timeout: 5000, intervals: [100, 200, 500, 1000] },
      )
      .toBe(true);

    expect(httpNotif).toBeDefined();
    expect(httpNotif!.type).toBe('follow');
    expect(httpNotif!.userId).toBe(follower.id);
  });
});
