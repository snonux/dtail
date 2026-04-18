package discovery

import "testing"

func TestNewParsesModuleOptionsWithAdditionalColons(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		method  string
		wantMod string
		wantOpt string
		wantErr bool
	}{
		{
			name:    "plain module",
			method:  "file",
			wantMod: "FILE",
		},
		{
			name:    "options with additional colons",
			method:  "method:host:port:extra",
			wantMod: "METHOD",
			wantOpt: "host:port:extra",
		},
		{
			name:    "empty options rejected",
			method:  "method:",
			wantErr: true,
		},
		{
			name:    "missing module rejected",
			method:  ":host:port",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := New(tt.method, "server", Shuffle)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("New(%q) error = nil, want error", tt.method)
				}
				return
			}
			if err != nil {
				t.Fatalf("New(%q) error = %v, want nil", tt.method, err)
			}
			if got.module != tt.wantMod {
				t.Fatalf("module = %q, want %q", got.module, tt.wantMod)
			}
			if got.options != tt.wantOpt {
				t.Fatalf("options = %q, want %q", got.options, tt.wantOpt)
			}
		})
	}
}
