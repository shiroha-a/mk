package autoscale

import "time"

// Clock abstracts time.Now so AIMDController can be exercised
// deterministically in unit tests via a fake clock. The interface is
// kept minimal — only Now is needed because cool-down + sustained-idle
// tracking are expressed as wall-clock differences inside the controller.
//
// 既存 mk-go コードでも同種の Clock interface パターンが複数箇所に存在
// する (e.g. core/twofactor の replay guard、core/timeline の cache window
// 計算)。本パッケージは依存を増やさないために自前の 1-method interface
// を持つ。fake clock implementation は autoscale_test.go に閉じる。
type Clock interface {
	Now() time.Time
}

// systemClock wraps time.Now into the Clock interface. Used as the
// default when callers do not supply their own Clock to NewAIMDController.
type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
