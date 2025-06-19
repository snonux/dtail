package config

import (
	"testing"

	"github.com/mimecast/dtail/internal/testutil"
)

func TestServerConfig(t *testing.T) {
	t.Run("default values", func(t *testing.T) {
		s := ServerConfig{}
		
		// Test zero values
		testutil.AssertEqual(t, "", s.SSHBindAddress)
		testutil.AssertEqual(t, 0, s.MaxConnections)
		testutil.AssertEqual(t, 0, s.MaxConcurrentCats)
		testutil.AssertEqual(t, 0, s.MaxConcurrentTails)
		testutil.AssertEqual(t, 0, len(s.Permissions.Default))
		testutil.AssertEqual(t, 0, len(s.Permissions.Users))
		testutil.AssertEqual(t, 0, len(s.Schedule))
		testutil.AssertEqual(t, 0, len(s.Continuous))
	})

	t.Run("user permissions", func(t *testing.T) {
		// Save original server config
		origServer := Server
		defer func() {
			Server = origServer
		}()
		
		// Set up test server config
		Server = &ServerConfig{
			Permissions: Permissions{
				Default: []string{"read:/tmp/.*"},
				Users: map[string][]string{
					"admin": {".*"},
					"user1": {"read:.*"},
					"user2": {"read:/var/log/.*"},
				},
			},
		}
		
		// Test existing users
		perms, err := ServerUserPermissions("admin")
		testutil.AssertNoError(t, err)
		testutil.AssertEqual(t, 1, len(perms))
		testutil.AssertEqual(t, ".*", perms[0])
		
		perms, err = ServerUserPermissions("user1")
		testutil.AssertNoError(t, err)
		testutil.AssertEqual(t, 1, len(perms))
		testutil.AssertEqual(t, "read:.*", perms[0])
		
		// Test non-existing user (should get default)
		perms, err = ServerUserPermissions("unknown")
		testutil.AssertNoError(t, err)
		testutil.AssertEqual(t, 1, len(perms))
		testutil.AssertEqual(t, "read:/tmp/.*", perms[0])
	})

	t.Run("no default permissions", func(t *testing.T) {
		// Save original server config
		origServer := Server
		defer func() {
			Server = origServer
		}()
		
		Server = &ServerConfig{
			Permissions: Permissions{
				Users: map[string][]string{
					"user1": {"read:.*"},
				},
			},
		}
		
		// Should get empty permissions for unknown user when no default
		_, err := ServerUserPermissions("unknown")
		testutil.AssertError(t, err, "Empty set of permission")
	})

	t.Run("empty permissions", func(t *testing.T) {
		// Save original server config
		origServer := Server
		defer func() {
			Server = origServer
		}()
		
		Server = &ServerConfig{}
		
		// Should error when no permissions configured
		_, err := ServerUserPermissions("anyone")
		testutil.AssertError(t, err, "Empty set of permission")
	})

	t.Run("max connections", func(t *testing.T) {
		s := ServerConfig{
			SSHBindAddress:     "0.0.0.0:2222",
			MaxConnections:     100,
			MaxConcurrentCats:  50,
			MaxConcurrentTails: 200,
			Permissions: Permissions{
				Users: map[string][]string{
					"user1": {"read:.*"},
				},
			},
		}
		
		testutil.AssertEqual(t, "0.0.0.0:2222", s.SSHBindAddress)
		testutil.AssertEqual(t, 100, s.MaxConnections)
		testutil.AssertEqual(t, 50, s.MaxConcurrentCats)
		testutil.AssertEqual(t, 200, s.MaxConcurrentTails)
	})

	t.Run("scheduled jobs", func(t *testing.T) {
		s := ServerConfig{
			Schedule: []Scheduled{
				{
					jobCommons: jobCommons{
						Name:  "cleanup",
						Files: "/tmp/*",
					},
					TimeRange: [2]int{0, 23},
				},
				{
					jobCommons: jobCommons{
						Name:  "health-check",
						Files: "/var/log/*",
					},
					TimeRange: [2]int{8, 17},
				},
			},
		}
		
		testutil.AssertEqual(t, 2, len(s.Schedule))
		testutil.AssertEqual(t, "cleanup", s.Schedule[0].Name)
		testutil.AssertEqual(t, "/tmp/*", s.Schedule[0].Files)
	})

	t.Run("SSH configuration", func(t *testing.T) {
		s := ServerConfig{
			KeyExchanges: []string{"diffie-hellman-group14-sha256"},
			Ciphers:      []string{"aes128-ctr", "aes256-ctr"},
			MACs:         []string{"hmac-sha2-256"},
		}
		
		testutil.AssertEqual(t, 1, len(s.KeyExchanges))
		testutil.AssertEqual(t, 2, len(s.Ciphers))
		testutil.AssertEqual(t, 1, len(s.MACs))
	})
	
	t.Run("default server config", func(t *testing.T) {
		s := newDefaultServerConfig()
		
		// Test default values
		testutil.AssertEqual(t, "0.0.0.0", s.SSHBindAddress)
		testutil.AssertEqual(t, "./cache/ssh_host_key", s.HostKeyFile)
		testutil.AssertEqual(t, "default", s.MapreduceLogFormat)
		testutil.AssertEqual(t, 1, len(s.Permissions.Default))
		testutil.AssertEqual(t, "^/.*", s.Permissions.Default[0])
		
		// Should have non-zero max values
		if s.MaxConnections == 0 {
			t.Error("Expected non-zero MaxConnections")
		}
		if s.MaxConcurrentCats == 0 {
			t.Error("Expected non-zero MaxConcurrentCats")
		}
		if s.MaxConcurrentTails == 0 {
			t.Error("Expected non-zero MaxConcurrentTails")
		}
	})
}