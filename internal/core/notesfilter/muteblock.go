package notesfilter

import (
	"fmt"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// MuteBlockSets holds the viewer-scoped exclusion sets used by
// ApplyMuteBlockChannel. They are resolved up front (one query each) and then
// applied in-memory to a fetched []*model.Note.
//
// 各 set は upstream Misskey TS の QueryService.generateBaseNoteFilteringQuery
// のうち muted-user / blocked-user の 2 次元 + ChannelMutingService の channel-mute
// に対応する (本 issue #1544 のスコープ)。generateBaseNoteFilteringQuery が併せ持つ
// muted-instances (mutedInstances host filter) / blocked-host / suspended-user の
// 各次元は本 issue のスコープ外で、別 follow-up で対応する:
//   - MutedUserIDs:    viewer が mute した user (active) — note/reply/renote
//     のいずれかの author なら除外 (generateMutedUserQueryForNotes 相当)
//   - BlockerIDs:      viewer を block している user — note/reply/renote の
//     いずれかの author なら除外 (generateBlockedUserQueryForNotes 相当、
//     「被 block」= blockeeId が viewer の行の blockerId 集合)
//   - MutedChannelIDs: viewer が mute した channel — note.channelId /
//     note.renoteChannelId が一致なら除外
type MuteBlockSets struct {
	MutedUserIDs    map[string]struct{}
	BlockerIDs      map[string]struct{}
	MutedChannelIDs map[string]struct{}
}

// LoadMuteBlockSets resolves the viewer's muted-user / blocker / muted-channel
// sets for use by ApplyMuteBlockChannel.
//
// Fail-closed: any repository error is returned to the caller so the endpoint
// can surface a 500 rather than silently serving notes that should have been
// filtered (security item, #1544). A nil viewer or a nil repo yields an empty
// (no-op) set for that dimension — anonymous viewers can't mute/block and an
// unwired repo means the feature is disabled in that deployment.
func LoadMuteBlockSets(
	viewer *model.User,
	mutingRepo repository.MutingRepository,
	blockingRepo repository.BlockingRepository,
	channelMutingRepo repository.ChannelMutingRepository,
) (MuteBlockSets, error) {
	sets := MuteBlockSets{}
	if viewer == nil {
		return sets, nil
	}

	if mutingRepo != nil {
		ids, err := mutingRepo.ListMuteeIDs(viewer.ID)
		if err != nil {
			return MuteBlockSets{}, fmt.Errorf("load muted users: %w", err)
		}
		sets.MutedUserIDs = toSet(ids)
	}

	if blockingRepo != nil {
		ids, err := blockingRepo.ListBlockerIDs(viewer.ID)
		if err != nil {
			return MuteBlockSets{}, fmt.Errorf("load blockers: %w", err)
		}
		sets.BlockerIDs = toSet(ids)
	}

	if channelMutingRepo != nil {
		rows, err := channelMutingRepo.ListByUser(viewer.ID)
		if err != nil {
			return MuteBlockSets{}, fmt.Errorf("load muted channels: %w", err)
		}
		if len(rows) > 0 {
			m := make(map[string]struct{}, len(rows))
			for _, row := range rows {
				m[row.ChannelID] = struct{}{}
			}
			sets.MutedChannelIDs = m
		}
	}

	return sets, nil
}

// ApplyMuteBlockChannel drops notes the viewer should not see because of a
// mute, a block, or a channel mute, mirroring upstream antennas/notes and
// roles/notes which call generateBaseNoteFilteringQuery + the muted-channel
// brackets after fetching the note ID set.
//
// A note is dropped when:
//   - its author, reply author, or renote author is in MutedUserIDs, OR
//   - its author, reply author, or renote author is in BlockerIDs
//     (= that user has blocked the viewer), OR
//   - its channelId or renoteChannelId is in MutedChannelIDs.
//
// The viewer's own posts are NOT exempted here: upstream applies the same
// query regardless, and a viewer can't mute/block themselves so self-posts
// pass through naturally. An all-empty MuteBlockSets is a no-op.
func ApplyMuteBlockChannel(notes []*model.Note, sets MuteBlockSets) []*model.Note {
	if len(notes) == 0 {
		return notes
	}
	if len(sets.MutedUserIDs) == 0 && len(sets.BlockerIDs) == 0 && len(sets.MutedChannelIDs) == 0 {
		return notes
	}

	out := make([]*model.Note, 0, len(notes))
	for _, n := range notes {
		if n == nil {
			continue
		}
		if userExcluded(sets.MutedUserIDs, n) || userExcluded(sets.BlockerIDs, n) {
			continue
		}
		if channelExcluded(sets.MutedChannelIDs, n) {
			continue
		}
		out = append(out, n)
	}
	return out
}

// userExcluded reports whether the note's author, reply author, or renote
// author is in the given exclusion set. Mirrors the note/reply/renote
// userId NOT IN (...) brackets in generateMutedUserQueryForNotes /
// generateBlockedUserQueryForNotes.
func userExcluded(set map[string]struct{}, n *model.Note) bool {
	if len(set) == 0 {
		return false
	}
	if _, hit := set[n.UserID]; hit {
		return true
	}
	if n.ReplyUserID != nil {
		if _, hit := set[*n.ReplyUserID]; hit {
			return true
		}
	}
	if n.RenoteUserID != nil {
		if _, hit := set[*n.RenoteUserID]; hit {
			return true
		}
	}
	return false
}

// channelExcluded reports whether the note's channelId or renoteChannelId is
// muted. Mirrors the two muted-channel brackets in upstream notes.ts.
func channelExcluded(set map[string]struct{}, n *model.Note) bool {
	if len(set) == 0 {
		return false
	}
	if n.ChannelID != nil {
		if _, hit := set[*n.ChannelID]; hit {
			return true
		}
	}
	if n.RenoteChannelID != nil {
		if _, hit := set[*n.RenoteChannelID]; hit {
			return true
		}
	}
	return false
}

func toSet(ids []string) map[string]struct{} {
	if len(ids) == 0 {
		return nil
	}
	m := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		m[id] = struct{}{}
	}
	return m
}
