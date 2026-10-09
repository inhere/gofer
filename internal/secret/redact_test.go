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

func TestRedactStringCommandLinePasswords(t *testing.T) {
	cases := []struct{ in, want string }{
		{"mysql -uroot -pS3cret db", "mysql -uroot -p" + Placeholder + " db"},
		{"mysqldump -h db -u app -p'p@ss word' shop", "mysqldump -h db -u app -p" + Placeholder + " shop"},
		{"curl -u alice:hunter2 https://x", "curl -u alice:" + Placeholder + " https://x"},
		{"curl -s --user bob:pw https://x", "curl -s --user bob:" + Placeholder + " https://x"},
		{"curl --user=bob:pw https://x", "curl --user=bob:" + Placeholder + " https://x"},
		{"psql postgres://app:pg-secret@db:5432/x", "psql postgres://app:" + Placeholder + "@db:5432/x"},
	}
	for _, tc := range cases {
		got, hit := RedactString(tc.in)
		if !hit || got != tc.want {
			t.Errorf("RedactString(%q) = (%q,%v), want %q", tc.in, got, hit, tc.want)
		}
	}
	// not passwords: a prompting -p, the -P port, docker -u uid:gid, curl -u without a password
	for _, in := range []string{"mysql -u root -p db", "mysql -P3306 -h db", "docker run -u 1000:1000 img", "curl -u alice https://x", "psql -p5432 -h db"} {
		if got, hit := RedactString(in); hit || got != in {
			t.Errorf("RedactString(%q) = (%q,%v), want unchanged", in, got, hit)
		}
	}
	// idempotent
	once, _ := RedactString("curl -u a:b https://x")
	if twice, hit := RedactString(once); hit || twice != once {
		t.Errorf("second pass changed %q -> %q", once, twice)
	}
}
