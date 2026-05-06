// #744 Phase 1 残: streaming WebSocket の local timeline channel。
//
// upstream Misskey TS と mk-go は `/streaming?i=<token>` で WebSocket を提供
// し、クライアントが `connect` で channel を subscribe したあと該当 event を
// push する。本 spec は localTimeline channel で:
//
//   1. signup user → WS connect → localTimeline subscribe
//   2. 同 user で public note を投稿 (HTTP POST /api/notes/create)
//   3. WS message として `{type:"channel", body:{id:<subId>, type:"note", body:<note>}}`
//      が届く
//   4. event の note.id が POST 結果の note.id と一致
//
// を assert する。streaming は Phase 1 残 spec の中で WebSocket 経路を担保
// する重要 path。channel の payload 形式 / id round-trip / event push 経路
// が両 backend で揃うことを担保する。
//
// 注: subscribe → publish の race を避けるため subscribe 後に短時間 sleep
// を入れている。upstream は subscribe ack を返さないので message 受信の
// timeout 内に event が届くかで成否を判定する。
//
// Phase 1 残のもう一つ (2FA-passkey) は別 spec。

import { expect, test } from '@playwright/test';
import { randomUsername, signupUser } from '../../fixtures/auth';
import { createNote } from '../../fixtures/notes';
import { resetRateLimit } from '../../fixtures/rate_limit';

const baseURL = process.env.MK_BASE_URL ?? 'http://mkgo:3000';
const wsURL = baseURL.replace(/^http/, 'ws');

test.describe('streaming: localTimeline', () => {
  test.beforeAll(() => {
    resetRateLimit();
  });

  test('subscribed user receives a note event for a local public note', async ({ request }) => {
    const me = await signupUser(request, randomUsername('strm'));
    const ws = new WebSocket(`${wsURL}/streaming?i=${me.token}`);

    // WS open を待つ。close (= server 側 reject) も即 fail として
    // 報告する (= test の 5s timeout 待ちを避け、原因を message に
    // 反映できる)。
    await new Promise<void>((resolve, reject) => {
      ws.addEventListener('open', () => resolve(), { once: true });
      ws.addEventListener('error', () => reject(new Error('ws connection error')), { once: true });
      ws.addEventListener(
        'close',
        (ev) => reject(new Error(`ws closed before open: code=${ev.code} reason=${ev.reason || '(empty)'}`)),
        { once: true },
      );
    });

    // localTimeline channel を subscribe。subId は client 任意で event の
    // round-trip 確認に使う。
    const subId = 'sub-' + Math.random().toString(16).slice(2);
    ws.send(JSON.stringify({
      type: 'connect',
      body: { channel: 'localTimeline', id: subId, params: {} },
    }));

    // 投稿 event 受信用の Promise を先に登録 (= 投稿 → 受信の race を防ぐ)。
    const noteEventPromise = new Promise<{ id: string; text?: string }>((resolve, reject) => {
      const handler = (ev: MessageEvent) => {
        let msg: { type?: string; body?: { id?: string; type?: string; body?: { id: string; text?: string } } };
        try {
          msg = JSON.parse(typeof ev.data === 'string' ? ev.data : '');
        } catch {
          return;
        }
        if (msg.type === 'channel' && msg.body?.id === subId && msg.body.type === 'note' && msg.body.body) {
          ws.removeEventListener('message', handler);
          resolve(msg.body.body);
        }
      };
      ws.addEventListener('message', handler);
      setTimeout(() => {
        ws.removeEventListener('message', handler);
        reject(new Error('did not receive note event within timeout'));
      }, 5000);
    });

    // subscribe 確定までの短時間バッファ。upstream は ack を返さないので
    // 厳密に待つ手段はなく、200ms で実用上十分。
    await new Promise((resolve) => setTimeout(resolve, 200));

    const note = await createNote(request, me.token, {
      text: 'streaming hello',
      visibility: 'public',
    });

    const evNote = await noteEventPromise;
    expect(evNote.id).toBe(note.id);

    ws.close();
  });
});
