package transfer

import (
	"bytes"
	"encoding/json"

	"github.com/shiroha-a/mk/internal/model"
)

// exportFavorites reuses the note export pipeline but pulls rows from
// NoteFavoriteRepository instead. Each favorite is unwrapped to its note so
// the output matches the notes export shape (easy round-trip).
//
// keyset pagination で順送りに取り込む (#424 でリポジトリ側を offset から
// untilID 方式に変更した移行)。collectClipNotes と同じパターン。
func (e *Exporter) exportFavorites(userID string) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('[')
	first := true
	untilID := ""
	for {
		rows, err := e.deps.NoteFavoriteRepo.ListByUser(userID, untilID, "", notesExportBatchSize)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		for _, fav := range rows {
			if fav.Note == nil {
				continue
			}
			entry := map[string]any{
				"id":        fav.ID,
				"favoritee": fav.UserID,
				"note":      e.packNoteForExport(fav.Note),
			}
			raw, jerr := json.Marshal(entry)
			if jerr != nil {
				return nil, jerr
			}
			if !first {
				buf.WriteByte(',')
			}
			buf.Write(raw)
			first = false
		}
		if len(rows) < notesExportBatchSize {
			break
		}
		// 結果は id DESC で帰ってくるので最後の (= 最も古い) ID を次の
		// untilID に渡して次ページを取得する。favorites は note_favorite.id で
		// ページネーションするので cursor も note_favorite.id (= rows[].ID)。
		untilID = rows[len(rows)-1].ID
	}
	buf.WriteByte(']')
	return buf.Bytes(), nil
}

// exportAntennas emits a JSON array with antenna settings suitable for re-import.
// 本家の ExportAntennasProcessorService と同じく keywords / excludeKeywords は
// 既に jsonb として保存されているので RawMessage のまま埋め込む。
func (e *Exporter) exportAntennas(userID string) ([]byte, error) {
	rows, err := e.deps.AntennaRepo.ListByUser(userID)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, a := range rows {
		entry := map[string]any{
			"name":            a.Name,
			"src":             string(a.Src),
			"keywords":        json.RawMessage(a.Keywords),
			"excludeKeywords": json.RawMessage(a.ExcludeKeywords),
			"users":           []string(a.Users),
			"caseSensitive":   a.CaseSensitive,
			"localOnly":       a.LocalOnly,
			"excludeBots":     a.ExcludeBots,
			"withReplies":     a.WithReplies,
			"withFile":        a.WithFile,
		}
		if a.UserListID != nil {
			entry["userListId"] = *a.UserListID
		}
		out = append(out, entry)
	}
	return json.Marshal(out)
}

// exportClips emits a JSON array of the user's clips, each with the packed
// notes contained in the clip. 各 clip 内の note は単純な配列 (本家互換)。
func (e *Exporter) exportClips(userID string) ([]byte, error) {
	clips, err := e.deps.ClipRepo.ListByUser(userID, "", "", 1000, 0)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(clips))
	for _, c := range clips {
		notes, err := e.collectClipNotes(c.ID)
		if err != nil {
			return nil, err
		}
		packed := make([]map[string]any, 0, len(notes))
		for _, n := range notes {
			packed = append(packed, e.packNoteForExport(n))
		}
		out = append(out, map[string]any{
			"id":          c.ID,
			"name":        c.Name,
			"description": c.Description,
			"isPublic":    c.IsPublic,
			"notes":       packed,
		})
	}
	return json.Marshal(out)
}

// collectClipNotes walks ClipNoteRepository with pagination and resolves each
// referenced note into its model.Note form for exporting.
func (e *Exporter) collectClipNotes(clipID string) ([]*model.Note, error) {
	var out []*model.Note
	untilID := ""
	for {
		rows, err := e.deps.ClipNoteRepo.ListByClip(clipID, untilID, "", 100)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		for _, cn := range rows {
			n, err := e.deps.NoteRepo.FindByIDWithRelations(cn.NoteID)
			if err != nil || n == nil {
				continue
			}
			out = append(out, n)
		}
		// ListByClip は note.id (= clip_note.noteId) で keyset ページネーションする
		// ため、次ページの cursor も note.id を渡す。clip_note.id を渡すと無関係な
		// ULID で比較されページ抜け/重複が起きる (#1950)。
		untilID = rows[len(rows)-1].NoteID
		if len(rows) < 100 {
			break
		}
	}
	return out, nil
}
