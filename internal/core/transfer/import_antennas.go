package transfer

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/datatypes"
)

// antennaImportEntry is the JSON shape produced by exportAntennas and
// accepted here on import. Only fields that map directly to model.Antenna
// are respected; unknown fields are ignored.
type antennaImportEntry struct {
	Name            string          `json:"name"`
	Src             string          `json:"src"`
	Keywords        json.RawMessage `json:"keywords"`
	ExcludeKeywords json.RawMessage `json:"excludeKeywords"`
	Users           []string        `json:"users"`
	UserListID      *string         `json:"userListId"`
	// UserListAccts は upstream ExportedAntenna の list source 用 acct 配列。import 側で
	// list→users 変換に使う (#2106 N24)。
	UserListAccts                  []string `json:"userListAccts"`
	CaseSensitive                  bool     `json:"caseSensitive"`
	LocalOnly                      bool     `json:"localOnly"`
	ExcludeBots                    bool     `json:"excludeBots"`
	WithReplies                    bool     `json:"withReplies"`
	WithFile                       bool     `json:"withFile"`
	ExcludeNotesInSensitiveChannel bool     `json:"excludeNotesInSensitiveChannel"`
}

// importAntennas decodes a JSON array of antenna entries and creates one
// antenna row per entry. 無効なエントリはスキップしてカウントする。
func (i *Importer) importAntennas(user *model.User, body []byte) (*ImportResult, error) {
	var entries []antennaImportEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("parse antennas json: %w", err)
	}
	res := &ImportResult{Total: len(entries)}
	for _, ent := range entries {
		if ent.Name == "" || ent.Src == "" {
			res.Skipped++
			continue
		}
		keywords := normalizeJSON(ent.Keywords, "[]")
		excludeKeywords := normalizeJSON(ent.ExcludeKeywords, "[]")

		// #2106 N24: upstream ImportAntennasProcessorService 互換 — list source antenna は
		// userListAccts を users source へ倒して取り込む (cross-instance では userListId が
		// 無意味なため)。userListAccts が無ければ src は維持する。
		src := model.AntennaSource(ent.Src)
		users := pq.StringArray(ent.Users)
		userListID := ent.UserListID
		if src == model.AntennaSourceList && len(ent.UserListAccts) > 0 {
			src = model.AntennaSourceUsers
			users = pq.StringArray(ent.UserListAccts)
			userListID = nil
		}
		if users == nil {
			users = pq.StringArray{}
		}

		antenna := &model.Antenna{
			ID:                             i.deps.IDGen.Generate(time.Now()),
			UserID:                         user.ID,
			Name:                           ent.Name,
			Src:                            src,
			UserListID:                     userListID,
			Users:                          users,
			Keywords:                       datatypes.JSON(keywords),
			ExcludeKeywords:                datatypes.JSON(excludeKeywords),
			CaseSensitive:                  ent.CaseSensitive,
			LocalOnly:                      ent.LocalOnly,
			ExcludeBots:                    ent.ExcludeBots,
			WithReplies:                    ent.WithReplies,
			WithFile:                       ent.WithFile,
			ExcludeNotesInSensitiveChannel: ent.ExcludeNotesInSensitiveChannel,
			IsActive:                       true,
			LastUsedAt:                     time.Now(),
		}
		if err := i.deps.AntennaRepo.Create(antenna); err != nil {
			res.Skipped++
			slog.Warn("transfer import: antenna create failed",
				"name", ent.Name, "err", err)
			continue
		}
		res.Applied++
	}
	return res, nil
}

// normalizeJSON returns raw if it's non-empty, otherwise the fallback literal.
// 本家の export フォーマットは keywords: [] を省略せず出力するが、念のため。
func normalizeJSON(raw json.RawMessage, fallback string) []byte {
	if len(raw) == 0 {
		return []byte(fallback)
	}
	return raw
}
