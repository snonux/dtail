package discovery

import (
	"testing"

	"github.com/mimecast/dtail/internal/io/dlog"
)

func TestMain(m *testing.M) {
	dlog.Common = &dlog.DLog{}
	m.Run()
}

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

func TestServerListIgnoresEmptyServerEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		method  string
		server  string
		want    []string
		wantErr bool
	}{
		{
			name:   "regex without module yields no phantom host",
			server: "/.*/",
			want:   []string{},
		},
		{
			name:   "comma list filters empty entries",
			server: "alpha,,beta,",
			want:   []string{"alpha", "beta"},
		},
		{
			name:   "empty server input preserves serverless sentinel",
			server: "",
			want:   []string{""},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := New(tt.method, tt.server, ServerOrder(99))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("New(%q, %q) error = nil, want error", tt.method, tt.server)
				}
				return
			}
			if err != nil {
				t.Fatalf("New(%q, %q) error = %v, want nil", tt.method, tt.server, err)
			}

			servers := got.ServerList()
			if len(servers) != len(tt.want) {
				t.Fatalf("ServerList() len = %d, want %d (%v)", len(servers), len(tt.want), servers)
			}
			for i := range tt.want {
				if servers[i] != tt.want[i] {
					t.Fatalf("ServerList()[%d] = %q, want %q (full=%v)", i, servers[i], tt.want[i], servers)
				}
			}
		})
	}
}
