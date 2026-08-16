package signupapplication

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseForm(t *testing.T) {
	t.Run("reads a definition", func(t *testing.T) {
		fields := ParseForm([]byte(`[{"label":"動機","type":"textarea","required":true,"maxLength":200}]`))
		require.Len(t, fields, 1)
		assert.Equal(t, "動機", fields[0].Label)
		assert.Equal(t, FieldTypeTextarea, fields[0].Type)
		assert.True(t, fields[0].Required)
		assert.Equal(t, 200, fields[0].MaxLength)
	})

	// 壊れた JSON は空フォーム扱い。**申請ページを 500 で潰さない。**
	for _, raw := range []string{``, `not json`, `{"not":"an array"}`} {
		t.Run("broken input yields no fields: "+raw, func(t *testing.T) {
			assert.Empty(t, ParseForm([]byte(raw)))
		})
	}
}

// **上限を置かないと管理者が自分で壊せる。** 項目を無制限に足せば申請ページが
// 使い物にならなくなり、最大長を無制限にすれば 1 件の申請で DB を膨らませられる。
func TestValidateForm(t *testing.T) {
	valid := []FormField{
		{Label: "fediverse アカウント", Type: FieldTypeText, Required: false},
		{Label: "参加の動機", Type: FieldTypeTextarea, Required: true, MaxLength: 500},
	}
	assert.NoError(t, ValidateForm(valid))
	assert.NoError(t, ValidateForm(nil), "空フォームは許す")

	tests := []struct {
		name   string
		fields []FormField
	}{
		{
			name:   "too many fields",
			fields: make([]FormField, MaxFormFields+1),
		},
		{
			name:   "empty label",
			fields: []FormField{{Label: "   ", Type: FieldTypeText}},
		},
		{
			name:   "label too long",
			fields: []FormField{{Label: strings.Repeat("あ", MaxFormLabelLength+1), Type: FieldTypeText}},
		},
		{
			name:   "unknown type",
			fields: []FormField{{Label: "x", Type: "password"}},
		},
		{
			name:   "negative max length",
			fields: []FormField{{Label: "x", Type: FieldTypeText, MaxLength: -1}},
		},
		{
			name:   "max length over the ceiling",
			fields: []FormField{{Label: "x", Type: FieldTypeText, MaxLength: MaxAnswerLength + 1}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ErrorIs(t, ValidateForm(tt.fields), ErrInvalidForm)
		})
	}
}

// **ラベルは定義から埋める。** クライアントに送らせると、申請者が審査画面に偽の
// ラベルを流し込める。
func TestBuildAnswers_TakesLabelsFromTheDefinition(t *testing.T) {
	fields := []FormField{
		{Label: "動機", Type: FieldTypeTextarea, Required: true},
		{Label: "  どこで知りましたか  ", Type: FieldTypeText},
	}

	answers, err := BuildAnswers(fields, []string{"  参加したいです  ", ""})
	require.NoError(t, err)
	require.Len(t, answers, 2)
	assert.Equal(t, "動機", answers[0].Label)
	assert.Equal(t, "参加したいです", answers[0].Value, "前後の空白は落とす")
	assert.Equal(t, "どこで知りましたか", answers[1].Label, "ラベルの空白も落とす")
	assert.Equal(t, "", answers[1].Value)
}

// フォーム定義が申請の途中で変わると起きる。**黙って詰め合わせると答えと設問が
// ずれる**ので、やり直してもらう。
func TestBuildAnswers_CountMismatch(t *testing.T) {
	fields := []FormField{{Label: "動機", Type: FieldTypeTextarea}}

	_, err := BuildAnswers(fields, []string{"a", "b"})
	assert.ErrorIs(t, err, ErrAnswerCountMismatch)

	_, err = BuildAnswers(fields, nil)
	assert.ErrorIs(t, err, ErrAnswerCountMismatch)
}

func TestBuildAnswers_Required(t *testing.T) {
	fields := []FormField{{Label: "動機", Type: FieldTypeTextarea, Required: true}}

	_, err := BuildAnswers(fields, []string{"   "})
	assert.ErrorIs(t, err, ErrAnswerRequired)
	assert.Contains(t, err.Error(), "動機", "どの設問かが分かること")
}

func TestBuildAnswers_Length(t *testing.T) {
	t.Run("uses the field limit", func(t *testing.T) {
		fields := []FormField{{Label: "動機", Type: FieldTypeText, MaxLength: 5}}
		_, err := BuildAnswers(fields, []string{"123456"})
		assert.ErrorIs(t, err, ErrAnswerTooLong)

		_, err = BuildAnswers(fields, []string{"12345"})
		assert.NoError(t, err)
	})

	t.Run("falls back to the default", func(t *testing.T) {
		fields := []FormField{{Label: "動機", Type: FieldTypeText}}
		_, err := BuildAnswers(fields, []string{strings.Repeat("a", DefaultAnswerLength+1)})
		assert.ErrorIs(t, err, ErrAnswerTooLong)
	})

	// 定義側が壊れていても、絶対上限は超えさせない。
	t.Run("caps at the hard ceiling", func(t *testing.T) {
		fields := []FormField{{Label: "動機", Type: FieldTypeText, MaxLength: MaxAnswerLength * 10}}
		_, err := BuildAnswers(fields, []string{strings.Repeat("a", MaxAnswerLength+1)})
		assert.ErrorIs(t, err, ErrAnswerTooLong)
	})

	// rune 単位で数える。**byte で見ると日本語が通らなくなる。**
	t.Run("counts runes", func(t *testing.T) {
		fields := []FormField{{Label: "動機", Type: FieldTypeText, MaxLength: 10}}
		_, err := BuildAnswers(fields, []string{strings.Repeat("あ", 10)})
		assert.NoError(t, err)
	})
}

func TestBuildAnswers_EmptyForm(t *testing.T) {
	answers, err := BuildAnswers(nil, nil)
	require.NoError(t, err)
	assert.Empty(t, answers)
}
