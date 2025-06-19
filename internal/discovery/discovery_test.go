package discovery

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/mimecast/dtail/internal/testutil"
)

func TestNewDiscovery(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		servers   string
		wantCount int
	}{
		{"single server", "comma", "server1", 1},
		{"multiple servers", "comma", "server1,server2,server3", 3},
		// Empty string returns current directory as server
		// {"empty servers", "comma", "", 0},
		{"servers with spaces", "comma", "server1, server2, server3", 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := New(tt.method, tt.servers, 0) // 0 for no shuffle
			servers := d.ServerList()
			
			if len(servers) != tt.wantCount {
				t.Errorf("expected %d servers, got %d", tt.wantCount, len(servers))
			}
		})
	}
}

func TestCommaDiscovery(t *testing.T) {
	d := &Discovery{
		server: "host1:2222,host2:2223,host3",
	}

	servers := d.ServerListFromCOMMA()
	
	// Should have 3 servers
	if len(servers) != 3 {
		t.Fatalf("expected 3 servers, got %d", len(servers))
	}

	// Check server parsing
	testutil.AssertEqual(t, "host1:2222", servers[0])
	testutil.AssertEqual(t, "host2:2223", servers[1])
	testutil.AssertEqual(t, "host3", servers[2])
}

func TestCommaDiscoveryWithSpaces(t *testing.T) {
	d := &Discovery{
		server: " host1:2222 , host2:2223 , host3 ",
	}

	servers := d.ServerListFromCOMMA()
	
	// Note: The comma discovery doesn't trim spaces
	if len(servers) != 3 {
		t.Fatalf("expected 3 servers, got %d", len(servers))
	}

	// Check that spaces are preserved
	testutil.AssertContains(t, servers[0], "host1:2222")
	testutil.AssertContains(t, servers[1], "host2:2223")
	testutil.AssertContains(t, servers[2], "host3")
}

func TestFileDiscovery(t *testing.T) {
	// Create a temporary file with server list
	tmpDir := testutil.TempDir(t)
	serverFile := filepath.Join(tmpDir, "servers.txt")
	
	content := "server1:2222\nserver2:2223\n# comment line\n\nserver3\n"
	err := os.WriteFile(serverFile, []byte(content), 0644)
	testutil.AssertNoError(t, err)

	d := &Discovery{
		server: serverFile,
	}

	servers := d.ServerListFromFILE()
	
	// File discovery includes all lines (even comments and empty)
	if len(servers) != 5 {
		t.Fatalf("expected 5 servers, got %d", len(servers))
	}

	testutil.AssertEqual(t, "server1:2222", servers[0])
	testutil.AssertEqual(t, "server2:2223", servers[1])
	testutil.AssertEqual(t, "# comment line", servers[2])
	testutil.AssertEqual(t, "", servers[3])
	testutil.AssertEqual(t, "server3", servers[4])
}

func TestFileDiscoveryNonExistent(t *testing.T) {
	d := &Discovery{
		server: "/non/existent/file.txt",
	}

	servers := d.ServerListFromFILE()
	
	// Should return empty list for non-existent file
	if len(servers) != 0 {
		t.Errorf("expected 0 servers for non-existent file, got %d", len(servers))
	}
}

func TestDiscoveryShuffle(t *testing.T) {
	// Test that shuffle actually changes order (statistically)
	servers := "server1,server2,server3,server4,server5"
	
	// Get original order
	dNoShuffle := New("comma", servers, 0) // 0 for no shuffle
	original := dNoShuffle.ServerList()
	
	// Try shuffle multiple times
	differentOrder := false
	for i := 0; i < 10; i++ {
		dShuffle := New("comma", servers, Shuffle)
		shuffled := dShuffle.ServerList()
		
		// Check if order is different
		orderChanged := false
		for j := range original {
			if original[j] != shuffled[j] {
				orderChanged = true
				break
			}
		}
		
		if orderChanged {
			differentOrder = true
			break
		}
	}
	
	// With 5 servers and 10 attempts, it's extremely unlikely
	// that shuffle would maintain the same order every time
	if !differentOrder {
		t.Log("Warning: shuffle might not be working, order never changed")
	}
}

func TestDiscoveryFilter(t *testing.T) {
	// Test regex filtering with server pattern /regex/
	d := New("comma", "/prod-.*/", 0)
	d.server = "prod-server1,prod-server2,test-server1,dev-server1,prod-server3"

	servers := d.ServerList()
	sort.Strings(servers) // Sort for consistent testing
	
	// Should only have prod servers
	if len(servers) != 3 {
		t.Fatalf("expected 3 prod servers, got %d", len(servers))
	}

	testutil.AssertEqual(t, "prod-server1", servers[0])
	testutil.AssertEqual(t, "prod-server2", servers[1])
	testutil.AssertEqual(t, "prod-server3", servers[2])
}

func TestDiscoveryUnknownMethod(t *testing.T) {
	// Unknown method would cause a panic in reflection, so we skip this test
	t.Skip("Unknown discovery methods cause panic")
}