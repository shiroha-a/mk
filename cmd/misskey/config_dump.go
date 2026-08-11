package main

import (
	"fmt"
	"os"

	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/server"
)

// runConfigDump prints the resolved configuration and exits.
//
// **サーバーを起動しない。** 設定を読むだけなので DB / Redis に繋がらなくても
// 動く。新規構築時や「本当にこの値で動いているのか」を確かめたいときに、
// 起動できない状態でも使えることが要点 (#2469)。
func runConfigDump(configPath string) int {
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config-dump: 設定を読めない: %v\n", err)
		return 1
	}
	role, err := config.ResolveProcessRole()
	if err != nil {
		// role の解決に失敗しても設定は出す。何が矛盾しているかを見たい
		// 場面なので、ここで止めると診断の役に立たない。
		fmt.Fprintf(os.Stderr, "config-dump: %v\n", err)
		role = config.RoleBoth
	}
	fmt.Print(server.RenderConfigDump(server.BuildConfigDump(cfg, role)))
	return 0
}
