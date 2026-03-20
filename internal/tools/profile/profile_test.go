package profile

import "testing"

func TestProfileDirFromArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "explicit profile dir",
			args: []string{"-profile", "-profiledir", "custom-profiles", "-plain"},
			want: "custom-profiles",
		},
		{
			name: "missing profile dir falls back to default",
			args: []string{"-profile", "-plain"},
			want: "profiles",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := profileDirFromArgs(tt.args); got != tt.want {
				t.Fatalf("profileDirFromArgs(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}
