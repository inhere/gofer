package secret

import "testing"

func TestRedactStringRedactsKVAndFlagShapes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "kv",
			in:   "api_key=sk-test-xxx",
			want: "api_key=" + Placeholder,
		},
		{
			name: "long flag",
			in:   "--api-key=sk-test-xxx",
			want: "--api-key=" + Placeholder,
		},
		{
			name: "short flag with space",
			in:   "-token sk-test-xxx",
			want: "-token " + Placeholder,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, hit := RedactString(tc.in)
			if !hit {
				t.Fatalf("RedactString(%q) hit=false, want true", tc.in)
			}
			if got != tc.want {
				t.Fatalf("RedactString(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRedactStringEmpty(t *testing.T) {
	got, hit := RedactString("")
	if got != "" || hit {
		t.Fatalf("RedactString empty = (%q,%v), want (empty,false)", got, hit)
	}
}
