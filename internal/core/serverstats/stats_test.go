package serverstats

import (
	"encoding/json"
	"net"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCollect(t *testing.T) {
	s := Collect()
	assert.NotEmpty(t, s.Machine)
	assert.Greater(t, s.CPU.Cores, 0)
	assert.Greater(t, s.Mem.Total, uint64(0))
	assert.Greater(t, s.FS.Total, uint64(0))
	// os / node は system info から常に埋まる (machine string 型)。
	assert.NotEmpty(t, s.OS)
	assert.NotEmpty(t, s.Node)
}

func TestCpuModel(t *testing.T) {
	m := cpuModel()
	assert.NotEqual(t, "", m)
}

func TestTotalMemory(t *testing.T) {
	mem := totalMemory()
	assert.Greater(t, mem, uint64(0))
}

func TestCollect_CPUModelNotEmpty(t *testing.T) {
	s := Collect()
	assert.NotEqual(t, "?", s.CPU.Model, "CPU model should be resolved on Linux")
}

// CollectPublic / EmptyPublic は machine/cpu/mem/fs のみ (公開エンドポイント)。
// os/node/psql/redis/net が漏れないことを JSON key で固定する (SI-1 regression guard)。
func TestPublicStatsShape(t *testing.T) {
	for _, ps := range []PublicStats{CollectPublic(), EmptyPublic()} {
		b, err := json.Marshal(ps)
		assert.NoError(t, err)
		var got map[string]any
		assert.NoError(t, json.Unmarshal(b, &got))
		keys := make([]string, 0, len(got))
		for k := range got {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		assert.Equal(t, []string{"cpu", "fs", "machine", "mem"}, keys, "公開 shape は 4 field のみ")
	}
}

func TestEmptyPublic(t *testing.T) {
	s := EmptyPublic()
	assert.Equal(t, "?", s.Machine)
	assert.Equal(t, "?", s.CPU.Model)
	assert.Equal(t, 0, s.CPU.Cores)
	assert.Equal(t, uint64(0), s.Mem.Total)
	assert.Equal(t, uint64(0), s.FS.Total)
}

func TestCollectPublic(t *testing.T) {
	s := CollectPublic()
	assert.NotEmpty(t, s.Machine)
	assert.Greater(t, s.CPU.Cores, 0)
}

func TestPickDefaultInterface(t *testing.T) {
	loopback := net.Interface{Name: "lo", Flags: net.FlagUp | net.FlagLoopback}
	down := net.Interface{Name: "eth-down", Flags: 0}
	noAddr := net.Interface{Name: "eth-noaddr", Flags: net.FlagUp}
	addrErr := net.Interface{Name: "eth-err", Flags: net.FlagUp}
	good := net.Interface{Name: "eth0", Flags: net.FlagUp}

	addrsOf := func(i net.Interface) ([]net.Addr, error) {
		switch i.Name {
		case "eth-noaddr":
			return nil, nil
		case "eth-err":
			return nil, assert.AnError
		case "eth0":
			return []net.Addr{&net.IPNet{IP: net.IPv4(10, 0, 0, 2)}}, nil
		}
		return nil, nil
	}

	// loopback/down/no-addr/addr-err は飛ばし、最初の有効 interface を返す。
	got := pickDefaultInterface([]net.Interface{loopback, down, noAddr, addrErr, good}, addrsOf)
	assert.Equal(t, "eth0", got)

	// 有効な interface が無ければ空文字。
	none := pickDefaultInterface([]net.Interface{loopback, down}, addrsOf)
	assert.Equal(t, "", none)
}
