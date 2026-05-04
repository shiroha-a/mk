package hashtag

import (
	"errors"
	"testing"

	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
)

// fakeRepo is an in-memory test double for repository.HashtagRepository.
// 単体テストでは DB を立てずに RecordMention の呼び出しを検証する。
type fakeRepo struct {
	calls    []recordCall
	errOn    string // RecordMention で error を返したい name (空なら全成功)
	attaches []recordCall
}

type recordCall struct {
	id, name, userID string
	isLocal          bool
}

func (f *fakeRepo) FindByName(string) (*model.Hashtag, error) { return nil, nil }
func (f *fakeRepo) RecordMention(idVal, name, userID string, isLocal bool) error {
	f.calls = append(f.calls, recordCall{id: idVal, name: name, userID: userID, isLocal: isLocal})
	if f.errOn != "" && f.errOn == name {
		return errors.New("forced")
	}
	return nil
}
func (f *fakeRepo) RecordAttach(idVal, name, userID string, isLocal bool) error {
	f.attaches = append(f.attaches, recordCall{id: idVal, name: name, userID: userID, isLocal: isLocal})
	return nil
}

func newTestService(t *testing.T) (*Service, *fakeRepo) {
	t.Helper()
	idGen, err := id.NewGenerator("aidx")
	if err != nil {
		t.Fatalf("id.NewGenerator: %v", err)
	}
	repo := &fakeRepo{}
	return NewService(repo, idGen), repo
}

func TestService_OnNoteCreated_LocalUser(t *testing.T) {
	s, repo := newTestService(t)
	user := &model.User{ID: "u1"} // Host == nil → local
	note := &model.Note{ID: "n1", UserID: "u1", Tags: pq.StringArray{"#go", "#test"}}

	s.OnNoteCreated(note, user)

	if len(repo.calls) != 2 {
		t.Fatalf("expected 2 RecordMention calls, got %d", len(repo.calls))
	}
	for i, want := range []string{"#go", "#test"} {
		if repo.calls[i].name != want {
			t.Errorf("call[%d].name = %q, want %q", i, repo.calls[i].name, want)
		}
		if repo.calls[i].userID != "u1" {
			t.Errorf("call[%d].userID = %q, want u1", i, repo.calls[i].userID)
		}
		if !repo.calls[i].isLocal {
			t.Errorf("call[%d].isLocal = false, want true", i)
		}
		if repo.calls[i].id == "" {
			t.Errorf("call[%d].id empty (idGen.Generate not called)", i)
		}
	}
}

func TestService_OnNoteCreated_RemoteUser(t *testing.T) {
	s, repo := newTestService(t)
	host := "remote.example"
	user := &model.User{ID: "u2", Host: &host}
	note := &model.Note{ID: "n2", UserID: "u2", Tags: pq.StringArray{"#federation"}}

	s.OnNoteCreated(note, user)

	if len(repo.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(repo.calls))
	}
	if repo.calls[0].isLocal {
		t.Errorf("isLocal = true for remote user (Host=%q)", host)
	}
}

func TestService_OnNoteCreated_SkipEmpty(t *testing.T) {
	cases := map[string]struct {
		note   *model.Note
		author *model.User
	}{
		"nil note":    {note: nil, author: &model.User{ID: "u"}},
		"nil author":  {note: &model.Note{Tags: pq.StringArray{"#x"}}, author: nil},
		"empty tags":  {note: &model.Note{Tags: pq.StringArray{}}, author: &model.User{ID: "u"}},
		"nil tags":    {note: &model.Note{}, author: &model.User{ID: "u"}},
		"empty entry": {note: &model.Note{Tags: pq.StringArray{""}}, author: &model.User{ID: "u"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s, repo := newTestService(t)
			s.OnNoteCreated(tc.note, tc.author)
			if len(repo.calls) != 0 {
				t.Errorf("expected 0 calls, got %d", len(repo.calls))
			}
		})
	}
}

func TestService_OnNoteCreated_NilService(t *testing.T) {
	// nil receiver でも panic しないこと (defensive guard)。
	var s *Service
	s.OnNoteCreated(&model.Note{Tags: pq.StringArray{"#x"}}, &model.User{ID: "u"})
}

func TestService_OnNoteCreated_NilRepo(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	s := &Service{repo: nil, idGen: idGen}
	// repo nil でも panic せず early return すること。
	s.OnNoteCreated(&model.Note{Tags: pq.StringArray{"#x"}}, &model.User{ID: "u"})
}

func TestService_OnNoteCreated_RepoError_BestEffort(t *testing.T) {
	// 1 つの tag で repo がエラーを返しても、後続 tag の処理は止めない。
	s, repo := newTestService(t)
	repo.errOn = "#fail"
	user := &model.User{ID: "u3"}
	note := &model.Note{Tags: pq.StringArray{"#ok1", "#fail", "#ok2"}}

	s.OnNoteCreated(note, user)

	if len(repo.calls) != 3 {
		t.Fatalf("all 3 RecordMention calls should be attempted, got %d", len(repo.calls))
	}
}
