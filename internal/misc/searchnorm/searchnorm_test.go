package searchnorm

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "lowercases ASCII", in: "Tokyo", want: "tokyo"},
		{name: "already normalized is unchanged", in: "golang", want: "golang"},
		{name: "NFKC folds full-width to half-width", in: "ＡＢＣ", want: "abc"},
		{name: "NFKC + lowercase combined", in: "Ｇｏ", want: "go"},
		{name: "empty stays empty", in: "", want: ""},
		{name: "non-latin is preserved", in: "東京", want: "東京"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Normalize(tc.in); got != tc.want {
				t.Fatalf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
