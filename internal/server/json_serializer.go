package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
)

// fastJSONSerializer is the echo JSONSerializer used for all c.JSON / c.Bind
// paths. It is intentionally backed by the stdlib `encoding/json`.
//
// 経緯 (#1142 で調査確定): #507 で goccy/go-json に差し替えたが、v0.10.6 が
// remote Renote-with-Files を含む timeline payload で `ptrToString` nil-deref
// panic を起こすため revert した (#542)。v0.10.6 は goccy の最新版であり
// bump で回避する道は無い (dead end)。bytedance/sonic も評価したが、重量級の
// JIT 依存 + arch 制約 + 独自 panic surface があり、JSON encode の限定的な
// perf gain に対して drop-in 互換性最優先の server には blast radius が大き
// すぎるため見送った。したがって stdlib を意図的な恒久選択とする。vetted な
// 高速 encoder が現れたら再検討する。
type fastJSONSerializer struct{}

// Serialize writes i as JSON to the response. indent が空でなければ
// pretty-print する (echo 標準と同じセマンティクス)。
func (fastJSONSerializer) Serialize(c echo.Context, i interface{}, indent string) error {
	enc := json.NewEncoder(c.Response())
	if indent != "" {
		enc.SetIndent("", indent)
	}
	return enc.Encode(i)
}

// Deserialize parses request body JSON into i. 不正な JSON は echo の
// HTTPError(400) に翻訳し、そのまま返すと echo の error handler に渡る。
func (fastJSONSerializer) Deserialize(c echo.Context, i interface{}) error {
	if err := json.NewDecoder(c.Request().Body).Decode(i); err != nil {
		var ute *json.UnmarshalTypeError
		if errors.As(err, &ute) {
			return echo.NewHTTPError(http.StatusBadRequest,
				fmt.Sprintf("Unmarshal type error: expected=%v, got=%v, field=%v, offset=%v",
					ute.Type, ute.Value, ute.Field, ute.Offset)).SetInternal(err)
		}
		var se *json.SyntaxError
		if errors.As(err, &se) {
			return echo.NewHTTPError(http.StatusBadRequest,
				fmt.Sprintf("Syntax error: offset=%v, error=%v", se.Offset, se.Error())).SetInternal(err)
		}
		return err
	}
	return nil
}
