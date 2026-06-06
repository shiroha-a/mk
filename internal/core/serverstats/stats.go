// Package serverstats provides server machine statistics (CPU, memory, disk,
// OS, network) for the /api/admin/server-info endpoint.
package serverstats

import (
	"net"
	"os"
	"runtime"
	"syscall"
)

// Stats holds server machine information matching Misskey's server-info response
// (server-info.ts res). machine/os/node/psql/redis are strings; cpu/mem/fs/net
// are nested objects. psql/redis are filled by the handler (they need DB / Redis
// access) and left empty by Collect.
type Stats struct {
	Machine string  `json:"machine"`
	OS      string  `json:"os"`
	Node    string  `json:"node"`
	Psql    string  `json:"psql"`
	Redis   string  `json:"redis"`
	CPU     CPUInfo `json:"cpu"`
	Mem     MemInfo `json:"mem"`
	FS      FSInfo  `json:"fs"`
	Net     NetInfo `json:"net"`
}

// CPUInfo holds CPU model and core count.
type CPUInfo struct {
	Model string `json:"model"`
	Cores int    `json:"cores"`
}

// MemInfo holds total physical memory in bytes.
type MemInfo struct {
	Total uint64 `json:"total"`
}

// FSInfo holds total and used disk space in bytes for the root filesystem.
type FSInfo struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

// NetInfo holds the default network interface name (upstream net.interface).
type NetInfo struct {
	Interface string `json:"interface"`
}

// Collect gathers current server statistics (system info only). Psql/Redis are
// left empty for the handler to populate from the DB / Redis connection.
func Collect() Stats {
	hostname, _ := os.Hostname()

	var fs FSInfo
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err == nil {
		fs.Total = stat.Blocks * uint64(stat.Bsize)
		fs.Used = (stat.Blocks - stat.Bfree) * uint64(stat.Bsize)
	}

	return Stats{
		Machine: hostname,
		// upstream は os.platform() (linux/darwin/...) と process.version。
		// Go 移植では runtime.GOOS / runtime.Version() が同等の情報。
		OS:   runtime.GOOS,
		Node: runtime.Version(),
		CPU: CPUInfo{
			Model: cpuModel(),
			Cores: runtime.NumCPU(),
		},
		Mem: MemInfo{Total: totalMemory()},
		FS:  fs,
		Net: NetInfo{Interface: defaultInterface()},
	}
}

// PublicStats is the reduced shape returned by the unauthenticated public
// /api/server-info endpoint (upstream server-info.ts res): machine/cpu/mem/fs
// only. os/node/psql/redis/net are admin-only and must NOT leak to anonymous
// callers.
type PublicStats struct {
	Machine string  `json:"machine"`
	CPU     CPUInfo `json:"cpu"`
	Mem     MemInfo `json:"mem"`
	FS      FSInfo  `json:"fs"`
}

// CollectPublic gathers the public-endpoint subset (machine/cpu/mem/fs).
func CollectPublic() PublicStats {
	s := Collect()
	return PublicStats{Machine: s.Machine, CPU: s.CPU, Mem: s.Mem, FS: s.FS}
}

// EmptyPublic returns the public placeholder shape used when
// enableServerMachineStats is false (upstream server-info.ts disabled branch).
func EmptyPublic() PublicStats {
	return PublicStats{
		Machine: "?",
		CPU:     CPUInfo{Model: "?", Cores: 0},
		Mem:     MemInfo{Total: 0},
		FS:      FSInfo{Total: 0, Used: 0},
	}
}

// defaultInterface returns the name of the first non-loopback, up interface with
// a unicast address — approximating upstream `networkInterfaceDefault()`. Empty
// when none is found.
func defaultInterface() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	return pickDefaultInterface(ifaces, func(i net.Interface) ([]net.Addr, error) { return i.Addrs() })
}

// pickDefaultInterface holds the filtering logic of defaultInterface in a pure,
// testable form: it skips down / loopback / address-less interfaces and returns
// the first remaining interface's name. addrsOf resolves an interface's
// addresses (injected so tests can drive each branch).
func pickDefaultInterface(ifaces []net.Interface, addrsOf func(net.Interface) ([]net.Addr, error)) string {
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := addrsOf(ifi)
		if err != nil || len(addrs) == 0 {
			continue
		}
		return ifi.Name
	}
	return ""
}
