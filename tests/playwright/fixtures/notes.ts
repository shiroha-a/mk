// #744 Phase 1 PR-3: notes endpoint helper.
// upstream Misskey TS と互換な /api/notes/{create,show,delete} を叩く。
// spec から共通的に使う薄いラッパで、特殊な field (poll / files) は直接
// callApi で送る (helper を肥らせない)。

import type { APIRequestContext } from '@playwright/test';
import { callApi } from './api';

export type Visibility = 'public' | 'home' | 'followers' | 'specified';

export interface NoteCreateInput {
  text?: string;
  cw?: string;
  visibility?: Visibility;
  visibleUserIds?: string[];
}

export interface CreatedNote {
  id: string;
  text: string | null;
  cw: string | null;
  userId: string;
  visibility: Visibility;
}

// createNote posts /api/notes/create with the given token + body and returns
// the resulting note's minimal shape. upstream Misskey TS の response は
// `{ createdNote: { id, text, cw, userId, visibility, ... } }` で、本 helper
// では spec が必要な field のみ抽出する (= drift しても spec が壊れにくい)。
export async function createNote(
  request: APIRequestContext,
  token: string,
  input: NoteCreateInput,
): Promise<CreatedNote> {
  const resp = await callApi(request, 'notes/create', { i: token, ...input });
  if (resp.status() !== 200) {
    throw new Error(`notes/create failed: ${resp.status()} ${await resp.text()}`);
  }
  const body = await resp.json();
  const n = body.createdNote ?? body; // 防衛: 一部経路で createdNote が
  // wrapper されない実装があった場合に備えて両方受け止める
  return {
    id: n.id,
    text: n.text ?? null,
    cw: n.cw ?? null,
    userId: n.userId,
    visibility: n.visibility,
  };
}

// showNote fetches /api/notes/show. Misskey is auth-aware: visibility=specified
// な note は対象外の viewer が引くと 404 相当の error を返す。本 helper は
// status と body を直接 caller に渡すので、negative path test でも使える
// (= 4xx を期待する spec から resp.status() を見る)。
export async function showNoteRaw(
  request: APIRequestContext,
  token: string | null,
  noteId: string,
) {
  const body: Record<string, unknown> = { noteId };
  if (token) {
    body.i = token;
  }
  return callApi(request, 'notes/show', body);
}

// deleteNote calls /api/notes/delete; upstream は 204 No Content を返す。
// 失敗 (= 自分の note ではない / not found) は 4xx で API error を返すので
// caller は resp.status() を確認する設計。
export async function deleteNote(
  request: APIRequestContext,
  token: string,
  noteId: string,
) {
  return callApi(request, 'notes/delete', { i: token, noteId });
}
