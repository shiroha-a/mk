package federation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stretchr/testify/assert"
)

// fitsColumn は rune 数で見て、NUL は長さに関わらず弾くこと (#2723)。
func TestFitsColumn(t *testing.T) {
	nul := string(rune(0))
	assert.True(t, fitsColumn("abc", 3))
	assert.False(t, fitsColumn("abcd", 3))
	// PostgreSQL の varchar はコードポイント数で数える。byte で見ると 3 倍になる。
	assert.True(t, fitsColumn(strings.Repeat("\u3042", 3), 3))
	assert.False(t, fitsColumn("a"+nul, 10), "NUL は長さに関わらず弾く")
	assert.True(t, fitsColumn("", 0))
}

// remoteURIValue は**切らずに捨てる** (#2723)。
//
// 途中で切った URI は別物で、取りに行っても無駄なうえ壊れた参照を保存する
// ことになる。`remoteMediaURL` と同じ判断。
func TestRemoteURIValue(t *testing.T) {
	nul := string(rune(0))
	long := "https://remote.example/" + strings.Repeat("a", 512)

	assert.Equal(t, "https://remote.example/u", remoteURIValue("https://remote.example/u", "user.inbox", "https://remote.example/u"))
	assert.Empty(t, remoteURIValue("https://remote.example/u", "user.inbox", long), "切らずに捨てること")
	assert.Empty(t, remoteURIValue("https://remote.example/u", "user.inbox", "https://h/"+nul), "NUL を含む URI を捨てること")
	assert.Empty(t, remoteURIValue("https://remote.example/u", "user.inbox", ""))

	// **切った値を返さない。** 切って返すと壊れた参照が保存される。
	got := remoteURIValue("https://remote.example/u", "user.inbox", long)
	assert.NotEqual(t, truncateRunes(long, userURIMaxRunes), got)
}

// actorDocWith builds an actor document with the given extra top-level fields.
func actorDocWith(host, name, extra string) string {
	base := "https://" + host + "/users/" + name
	return `{"@context":"https://www.w3.org/ns/activitystreams","id":"` + base +
		`","type":"Person","preferredUsername":"` + name + `","inbox":"` + base + `/inbox",` + extra +
		`"publicKey":{"id":"` + base + `#main-key","owner":"` + base +
		`","publicKeyPem":"-----BEGIN PUBLIC KEY-----\nFAKE\n-----END PUBLIC KEY-----"}}`
}

// 収まらない任意の URI は捨てて、actor 自体は取り込むこと (#2723)。
//
// 切らずに捨てるのは、途中で切った URI が別物になるから。捨てずに落とすと
// **actor が 1 行も作られず**、refresh も同じ失敗を繰り返す。
func TestResolveActor_DropsOversizedOptionalURIs(t *testing.T) {
	long := "https://remote.example/" + strings.Repeat("a", 512)
	r, _ := ingestCWEnv(t)
	f := r.fetcher.(*countingFetcher)
	f.docs["https://remote.example/users/big"] = actorDocWith("remote.example", "big",
		`"featured":"`+long+`","movedTo":"`+long+`",`)

	user, err := r.ResolveActor("https://remote.example/users/big")
	require.NoError(t, err, "収まらない任意 URI で actor ごと落ちている")
	require.NotNil(t, user)
	assert.Nil(t, user.Featured, "収まらない featured を保存している")
	assert.Nil(t, user.MovedToURI, "収まらない movedToUri を保存している")
	// 身元は残る。
	require.NotNil(t, user.URI)
	assert.Equal(t, "https://remote.example/users/big", *user.URI)
}

// 身元が列に入らない actor は拒否すること (#2723)。
//
// `uri` / `host` / `username` は切ると別人になるし、捨てるわけにもいかない
// (NOT NULL / lookup の鍵)。ここで弾かないと Create が落ちて同じ失敗を繰り返す。
func TestResolveActor_RejectsOversizedIdentity(t *testing.T) {
	r, _ := ingestCWEnv(t)
	f := r.fetcher.(*countingFetcher)
	// **username は既存の validRemoteUsername が 128 で弾く**ので、そちらでは
	// この gate に到達しない。uri が列 (varchar(512)) を超える形で確かめる。
	uri := "https://remote.example/users/u" + strings.Repeat("q", 520)
	f.docs[uri] = actorDocWith2(uri, "shortname")

	_, err := r.ResolveActor(uri)
	require.Error(t, err, "身元が列に入らない actor を受理している")
	assert.ErrorIs(t, err, ErrInvalidActor)

	// **host も見る。** uri が 512 に収まっていても host が 128 を超えることは
	// ありうる (DNS の上限は 253)。uri だけ見る実装だと素通りする。
	longHost := strings.Repeat("h", 200) + ".example"
	hostURI := "https://" + longHost + "/users/x"
	require.Less(t, len([]rune(hostURI)), userURIMaxRunes, "前提: uri は列に収まる")
	f.docs[hostURI] = actorDocWith2(hostURI, "x")

	_, err = r.ResolveActor(hostURI)
	require.Error(t, err, "host が列に入らない actor を受理している")
	assert.ErrorIs(t, err, ErrInvalidActor)
}

// actorDocWith2 builds an actor document with an explicit id.
func actorDocWith2(id, name string) string {
	return `{"@context":"https://www.w3.org/ns/activitystreams","id":"` + id +
		`","type":"Person","preferredUsername":"` + name + `","inbox":"` + id + `/inbox",` +
		`"publicKey":{"id":"` + id + `#main-key","owner":"` + id +
		`","publicKeyPem":"-----BEGIN PUBLIC KEY-----\nFAKE\n-----END PUBLIC KEY-----"}}`
}
