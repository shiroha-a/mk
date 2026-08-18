package pgarray

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 期待値は lib/pq の実装と突き合わせて確定させたもの (#2628)。取り込み元と
// 挙動が変わっていないことを固定するのが目的なので、値を「こうあるべき」で
// 書き換えないこと。DB に既に入っているリテラルを読めなくなる。

func TestStringArray_Value(t *testing.T) {
	tests := []struct {
		name string
		in   StringArray
		want any
	}{
		{name: "nil is SQL NULL", in: nil, want: nil},
		{name: "empty", in: StringArray{}, want: `{}`},
		{name: "single", in: StringArray{"a"}, want: `{"a"}`},
		{name: "multiple", in: StringArray{"a", "b"}, want: `{"a","b"}`},
		{name: "empty element", in: StringArray{""}, want: `{""}`},
		{name: "delimiter inside", in: StringArray{"a,b"}, want: `{"a,b"}`},
		{name: "quote inside", in: StringArray{`a"b`}, want: `{"a\"b"}`},
		{name: "backslash inside", in: StringArray{`a\b`}, want: `{"a\\b"}`},
		{name: "braces inside", in: StringArray{"{}"}, want: `{"{}"}`},
		{name: "literal NULL string", in: StringArray{"NULL"}, want: `{"NULL"}`},
		{name: "multibyte", in: StringArray{"日本語"}, want: `{"日本語"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.in.Value()
			require.NoError(t, err)
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestStringArray_Scan(t *testing.T) {
	tests := []struct {
		name    string
		src     any
		want    StringArray
		wantNil bool
		wantErr bool
	}{
		{name: "SQL NULL", src: nil, wantNil: true},
		{name: "empty", src: []byte(`{}`), want: StringArray{}},
		{name: "single", src: []byte(`{"a"}`), want: StringArray{"a"}},
		{name: "unquoted", src: []byte(`{a,b}`), want: StringArray{"a", "b"}},
		{name: "delimiter inside", src: []byte(`{"a,b"}`), want: StringArray{"a,b"}},
		{name: "quote inside", src: []byte(`{"a\"b"}`), want: StringArray{`a"b`}},
		{name: "backslash inside", src: []byte(`{"a\\b"}`), want: StringArray{`a\b`}},
		{name: "multibyte", src: []byte(`{"日本語"}`), want: StringArray{"日本語"}},
		// 要素の SQL NULL は受け付けない (取り込み元の挙動)。mk-go の配列カラムは
		// すべて NOT NULL DEFAULT '{}' なので、実データでは到達しない。
		{name: "NULL element", src: []byte(`{a,NULL,c}`), wantErr: true},
		// string でも受け取れる。
		{name: "string source", src: `{a}`, want: StringArray{"a"}},
		{name: "malformed: empty", src: []byte(``), wantErr: true},
		{name: "malformed: open only", src: []byte(`{`), wantErr: true},
		{name: "malformed: close only", src: []byte(`}`), wantErr: true},
		{name: "malformed: not an array", src: []byte(`abc`), wantErr: true},
		{name: "malformed: nested", src: []byte(`{{a},{b}}`), wantErr: true},
		{name: "unsupported type", src: 42, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got StringArray
			err := got.Scan(tt.src)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.wantNil {
				assert.Nil(t, got)
				return
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestInt64Array_Value(t *testing.T) {
	tests := []struct {
		name string
		in   Int64Array
		want any
	}{
		{name: "nil is SQL NULL", in: nil, want: nil},
		{name: "empty", in: Int64Array{}, want: `{}`},
		{name: "single", in: Int64Array{1}, want: `{1}`},
		{name: "multiple", in: Int64Array{1, 2, 3}, want: `{1,2,3}`},
		{name: "negative", in: Int64Array{-1}, want: `{-1}`},
		{name: "max", in: Int64Array{9223372036854775807}, want: `{9223372036854775807}`},
		{name: "min", in: Int64Array{-9223372036854775808}, want: `{-9223372036854775808}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.in.Value()
			require.NoError(t, err)
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestInt64Array_Scan(t *testing.T) {
	tests := []struct {
		name    string
		src     any
		want    Int64Array
		wantNil bool
		wantErr bool
	}{
		{name: "SQL NULL", src: nil, wantNil: true},
		{name: "empty", src: []byte(`{}`), want: Int64Array{}},
		{name: "single", src: []byte(`{1}`), want: Int64Array{1}},
		{name: "multiple", src: []byte(`{1,2,3}`), want: Int64Array{1, 2, 3}},
		{name: "negative", src: []byte(`{-1}`), want: Int64Array{-1}},
		{name: "string source", src: `{7}`, want: Int64Array{7}},
		{name: "malformed: not a number", src: []byte(`{a}`), wantErr: true},
		{name: "malformed: open only", src: []byte(`{`), wantErr: true},
		{name: "unsupported type", src: 4.2, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got Int64Array
			err := got.Scan(tt.src)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.wantNil {
				assert.Nil(t, got)
				return
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// Value した結果を Scan で読み戻せること。DB を跨いだ往復と同じ経路になる。
func TestRoundTrip(t *testing.T) {
	for _, in := range []StringArray{
		{},
		{"a"},
		{"a", "b", "c"},
		{""},
		{"a,b", `c"d`, `e\f`, "{}", "NULL", "日本語", " 前後 ", "改行\n入り"},
	} {
		v, err := in.Value()
		require.NoError(t, err)
		var back StringArray
		require.NoError(t, back.Scan([]byte(v.(string))))
		assert.Equal(t, []string(in), []string(back), "round trip changed the value")
	}

	for _, in := range []Int64Array{{}, {0}, {1, -2, 3}} {
		v, err := in.Value()
		require.NoError(t, err)
		var back Int64Array
		require.NoError(t, back.Scan([]byte(v.(string))))
		assert.Equal(t, []int64(in), []int64(back))
	}
}

// scanLinearArray は多次元配列を受け付けない (mk-go のカラムは全て 1 次元)。
func TestScanLinearArray_RejectsMultiDimensional(t *testing.T) {
	_, err := scanLinearArray([]byte(`{{1,2},{3,4}}`), []byte{','}, "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot convert ARRAY")
}

func TestParseArray_Errors(t *testing.T) {
	for _, src := range []string{``, `x`, `{`, `{a`, `{a,`, `{"a}`, `{a}x`} {
		_, _, err := parseArray([]byte(src), []byte{','})
		require.Error(t, err, "src=%q should fail to parse", src)
	}
}

func TestAppendArrayQuotedBytes(t *testing.T) {
	assert.Equal(t, `"a"`, string(appendArrayQuotedBytes(nil, []byte("a"))))
	assert.Equal(t, `"a\"b"`, string(appendArrayQuotedBytes(nil, []byte(`a"b`))))
	assert.Equal(t, `"a\\b"`, string(appendArrayQuotedBytes(nil, []byte(`a\b`))))
	assert.Equal(t, `""`, string(appendArrayQuotedBytes(nil, nil)))
}
