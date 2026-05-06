// #744 Phase 1 残: drive/files/create の正常系。
//
// upstream Misskey TS と mk-go の `/api/drive/files/create` は
// multipart/form-data で file を受け取り、drive_file row を作って
// packed shape を返す。本 spec は両 backend 共通で:
//
//   1. 1x1 transparent PNG を multipart upload
//   2. response が 200 + 期待 shape (id/name/type/url/size)
//   3. drive/files/show で同 file を取得して整合性確認
//
// を assert する。
//
// 注: `userId` は drift がある (upstream は `withUser=false` で常時 null、
// mk-go は owner ID を返す)。本 spec では assert しない。drop-in 互換 fix
// は別 issue (#812) で tracking。fix 後は本 spec の userId assertion を
// 復活させる。
//
// Phase 1 残 spec の中で最も isolation が clean (= signup 経路で fresh
// user / 他 spec への副作用なし) なため最初に入れる。streaming /
// 2FA-passkey は別 spec で個別に整備する。

import { expect, test } from '@playwright/test';
import { callApi } from '../../fixtures/api';
import { randomUsername, signupUser } from '../../fixtures/auth';
import { resetRateLimit } from '../../fixtures/rate_limit';

const baseURL = process.env.MK_BASE_URL ?? 'http://mkgo:3000';

// 1x1 transparent PNG, 67 bytes。test 内で独自 image を生成すると node の
// Sharp/Canvas 依存が増えるので、固定の minimal PNG を base64 で持つ方が
// 楽 + deterministic。
const tinyPNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=',
  'base64',
);

test.describe('drive: files/create', () => {
  test.beforeAll(() => {
    resetRateLimit();
  });

  test('uploads a tiny PNG and exposes it via files/show', async ({ request }) => {
    const me = await signupUser(request, randomUsername('drv'));

    const uploadResp = await request.post(`${baseURL}/api/drive/files/create`, {
      multipart: {
        i: me.token,
        file: {
          name: 'tiny.png',
          mimeType: 'image/png',
          buffer: tinyPNG,
        },
      },
      failOnStatusCode: false,
    });
    expect(uploadResp.status()).toBe(200);
    const file = await uploadResp.json();
    expect(file.id).toBeTruthy();
    expect(file.name).toBe('tiny.png');
    expect(file.type).toBe('image/png');
    // url は scheme 必須で strict assert する。文字列断片や path-only な値が
    // 混入する regression を弾く (= scheme は backend config 依存で http/https
    // どちらも valid)。
    expect(file.url).toMatch(/^https?:\/\//);
    expect(file.size).toBe(tinyPNG.length);

    // files/show で同 file を取得して shape 整合を確認。create と show で
    // identifier 系 (id) だけでなく metadata (name/type/size) も一致する
    // ことを assert することで、内部 state が正しく persist されたことを
    // strict に担保する。
    const showResp = await callApi(request, 'drive/files/show', {
      i: me.token,
      fileId: file.id,
    });
    expect(showResp.status()).toBe(200);
    const got = await showResp.json();
    expect(got.id).toBe(file.id);
    expect(got.name).toBe(file.name);
    expect(got.type).toBe(file.type);
    expect(got.size).toBe(file.size);
  });
});
