package langmap

import "testing"

func TestIsValid(t *testing.T) {
	valid := []string{"ja-JP", "en-US", "ach", "zh-Hans", "zu-ZA", "en@pirate"}
	for _, c := range valid {
		if !IsValid(c) {
			t.Errorf("expected %q to be a valid lang code", c)
		}
	}
	invalid := []string{"klingon", "", "JA", "xx-YY", "ja_JP"}
	for _, c := range invalid {
		if IsValid(c) {
			t.Errorf("expected %q to be rejected", c)
		}
	}
}

func TestCodes_NonEmpty(t *testing.T) {
	if len(codes) == 0 {
		t.Fatal("langmap codes must not be empty")
	}
}
