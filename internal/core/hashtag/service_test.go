package hashtag

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
)

// fakeRepo is an in-memory test double for repository.HashtagRepository.
// 単体テストでは DB を立てずに RecordMention の呼び出しを検証する。
//
// #719 で Service.OnNoteCreated が goroutine spawn になったため、複数
// goroutine から同時 RecordMention されても race にならないよう
// sync.Mutex で calls / attaches を guard する。
type fakeRepo struct {
	mu       sync.Mutex
	calls    []recordCall
	errOn    string // RecordMention で error を返したい name (空なら全成功)
	attaches []recordCall
}

type recordCall struct {
	id, name, userID string
	isLocal          bool
}

func (f *fakeRepo) snapshotCalls() []recordCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeRepo) FindByName(string) (*model.Hashtag, error) { return nil, nil }
func (f *fakeRepo) RecordMention(idVal, name, userID string, isLocal bool) error {
	f.mu.Lock()
	f.calls = append(f.calls, recordCall{id: idVal, name: name, userID: userID, isLocal: isLocal})
	errOn := f.errOn
	f.mu.Unlock()
	if errOn != "" && errOn == name {
		return errors.New("forced")
	}
	return nil
}
func (f *fakeRepo) RecordAttach(idVal, name, userID string, isLocal bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	s.WaitForPendingWrites()

	calls := repo.snapshotCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 RecordMention calls, got %d", len(calls))
	}
	for i, want := range []string{"#go", "#test"} {
		if calls[i].name != want {
			t.Errorf("call[%d].name = %q, want %q", i, calls[i].name, want)
		}
		if calls[i].userID != "u1" {
			t.Errorf("call[%d].userID = %q, want u1", i, calls[i].userID)
		}
		if !calls[i].isLocal {
			t.Errorf("call[%d].isLocal = false, want true", i)
		}
		if calls[i].id == "" {
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
	s.WaitForPendingWrites()

	calls := repo.snapshotCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].isLocal {
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
			s.WaitForPendingWrites()
			if calls := repo.snapshotCalls(); len(calls) != 0 {
				t.Errorf("expected 0 calls, got %d", len(calls))
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
	repo.mu.Lock()
	repo.errOn = "#fail"
	repo.mu.Unlock()
	user := &model.User{ID: "u3"}
	note := &model.Note{Tags: pq.StringArray{"#ok1", "#fail", "#ok2"}}

	s.OnNoteCreated(note, user)
	s.WaitForPendingWrites()

	if calls := repo.snapshotCalls(); len(calls) != 3 {
		t.Fatalf("all 3 RecordMention calls should be attempted, got %d", len(calls))
	}
}

// TestService_OnNoteCreated_ReturnsImmediately は #719 で導入した async
// 化の振る舞いを保証する: caller は repo の RecordMention 完了を待たずに
// 即時 return できる。fakeRepo に意図的な sleep を入れて、それでも
// OnNoteCreated 自体が ms オーダーで戻ることを assert する。
func TestService_OnNoteCreated_ReturnsImmediately(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	slow := &slowRepo{block: make(chan struct{})}
	s := NewService(slow, idGen)

	user := &model.User{ID: "u1"}
	note := &model.Note{Tags: pq.StringArray{"#a"}}

	start := time.Now()
	s.OnNoteCreated(note, user)
	elapsed := time.Since(start)
	// goroutine spawn は通常 1ms 未満で戻る。100ms ゆとりを取って async
	// 化が壊れた regression を guard する。
	if elapsed > 100*time.Millisecond {
		t.Fatalf("OnNoteCreated should return immediately, took %v", elapsed)
	}
	close(slow.block) // worker goroutine を unblock して flake 防止
	s.WaitForPendingWrites()
}

// slowRepo は RecordMention で意図的に block して sync 経路を再現する。
// async 化の regression test 専用。
type slowRepo struct {
	block chan struct{}
}

func (r *slowRepo) FindByName(string) (*model.Hashtag, error)  { return nil, nil }
func (r *slowRepo) RecordMention(_, _, _ string, _ bool) error { <-r.block; return nil }
func (r *slowRepo) RecordAttach(_, _, _ string, _ bool) error  { return nil }

func TestService_WaitForPendingWrites_NilReceiver(t *testing.T) {
	// nil *Service でも panic せず即 return すること。production code は
	// 呼ばないが、test infra の defensive check。
	var s *Service
	s.WaitForPendingWrites()
}

// pending worker が既に drain 済み (or 0 件) なら Shutdown は ctx 関係なく
// 即 return する (#727)。
func TestService_Shutdown_NoPendingReturnsImmediately(t *testing.T) {
	s, _ := newTestService(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown should succeed when nothing is pending: %v", err)
	}
}

// in-flight worker があるとき、ctx 期限切れ前に worker が drain すれば
// nil error を返す。
func TestService_Shutdown_DrainsPending(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := &slowRepo{block: make(chan struct{})}
	s := &Service{repo: repo, idGen: idGen}
	user := &model.User{ID: "u1"}
	note := &model.Note{Tags: pq.StringArray{"#a"}}
	s.OnNoteCreated(note, user)

	go func() {
		time.Sleep(10 * time.Millisecond)
		close(repo.block) // worker を unblock
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown should drain in time: %v", err)
	}
}

// ctx が期限切れになると Shutdown は ctx.Err() を返す (worker は drain
// 待たず諦める)。production の SIGTERM 時に shutdown timeout を超えて
// 待ち続けないことの guard。
func TestService_Shutdown_RespectsContextDeadline(t *testing.T) {
	idGen, _ := id.NewGenerator("aidx")
	repo := &slowRepo{block: make(chan struct{})}
	s := &Service{repo: repo, idGen: idGen}
	user := &model.User{ID: "u1"}
	note := &model.Note{Tags: pq.StringArray{"#a"}}
	s.OnNoteCreated(note, user)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := s.Shutdown(ctx)
	if err == nil {
		t.Fatal("Shutdown should return ctx.Err() when worker is still blocked")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown should return DeadlineExceeded, got %v", err)
	}
	close(repo.block) // cleanup: worker を unblock して goroutine leak 防止
}

// nil *Service の Shutdown は no-op。
func TestService_Shutdown_NilReceiver(t *testing.T) {
	var s *Service
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("nil Shutdown should be no-op: %v", err)
	}
}
