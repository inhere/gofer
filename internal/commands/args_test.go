package commands

import (
	"reflect"
	"testing"
)

func TestNormalizeArgs(t *testing.T) {
	app := NewApp("test")

	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "key before flags is moved after",
			in:   []string{"project", "add", "self", "--host-path", "/x", "--allow-exec"},
			want: []string{"project", "add", "--host-path", "/x", "--allow-exec", "self"},
		},
		{
			name: "flags-first unchanged",
			in:   []string{"project", "add", "--host-path", "/x", "self"},
			want: []string{"project", "add", "--host-path", "/x", "self"},
		},
		{
			name: "validate key before flag",
			in:   []string{"project", "validate", "self", "--config", "c.yaml"},
			want: []string{"project", "validate", "--config", "c.yaml", "self"},
		},
		{
			name: "no flags unchanged",
			in:   []string{"project", "show", "self"},
			want: []string{"project", "show", "self"},
		},
		{
			name: "first token is flag unchanged",
			in:   []string{"--help"},
			want: []string{"--help"},
		},
		{
			name: "double-dash separator preserved",
			in:   []string{"job", "run", "self", "--", "go", "version"},
			want: []string{"job", "run", "self", "--", "go", "version"},
		},
		{
			name: "empty",
			in:   []string{},
			want: []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeArgs(app, tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("NormalizeArgs(%v)\n got=%v\nwant=%v", tc.in, got, tc.want)
			}
		})
	}
}
