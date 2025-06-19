package config

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/testutil"
)

func TestArgs(t *testing.T) {
	t.Run("default values", func(t *testing.T) {
		args := Args{}
		
		// Test zero values
		testutil.AssertEqual(t, false, args.Quiet)
		testutil.AssertEqual(t, false, args.Plain)
		testutil.AssertEqual(t, false, args.Serverless)
		testutil.AssertEqual(t, false, args.NoColor)
		testutil.AssertEqual(t, false, args.RegexInvert)
		testutil.AssertEqual(t, "", args.SSHPrivateKeyFilePath)
		testutil.AssertEqual(t, false, args.TrustAllHosts)
		testutil.AssertEqual(t, 0, args.ConnectionsPerCPU)
		testutil.AssertEqual(t, "", args.ServersStr)
		testutil.AssertEqual(t, "", args.What)
		testutil.AssertEqual(t, "", args.QueryStr)
		testutil.AssertEqual(t, "", args.RegexStr)
		testutil.AssertEqual(t, 0, args.SSHPort)
		testutil.AssertEqual(t, omode.Mode(0), args.Mode)
	})

	t.Run("serialize options", func(t *testing.T) {
		args := Args{
			Quiet:      true,
			Plain:      true,
			Serverless: false,
			LContext: lcontext.LContext{
				MaxCount:      10,
				BeforeContext: 2,
				AfterContext:  3,
			},
		}
		
		// Serialize
		serialized := args.SerializeOptions()
		testutil.AssertContains(t, serialized, "quiet=true")
		testutil.AssertContains(t, serialized, "plain=true")
		testutil.AssertContains(t, serialized, "max=10")
		testutil.AssertContains(t, serialized, "before=2")
		testutil.AssertContains(t, serialized, "after=3")
		// serverless=false should not be included
		if strings.Contains(serialized, "serverless") {
			t.Error("serverless=false should not be serialized")
		}
	})

	t.Run("deserialize options", func(t *testing.T) {
		options := []string{
			"quiet=true",
			"plain=true",
			"before=5",
			"after=3",
			"max=100",
		}
		
		opts, ltx, err := DeserializeOptions(options)
		testutil.AssertNoError(t, err)
		
		// Check parsed options
		testutil.AssertEqual(t, "true", opts["quiet"])
		testutil.AssertEqual(t, "true", opts["plain"])
		
		// Check lcontext values
		testutil.AssertEqual(t, 5, ltx.BeforeContext)
		testutil.AssertEqual(t, 3, ltx.AfterContext)
		testutil.AssertEqual(t, 100, ltx.MaxCount)
	})

	t.Run("deserialize with base64", func(t *testing.T) {
		// Create a base64 encoded value
		testValue := "test pattern with spaces"
		encoded := "base64%" + base64.StdEncoding.EncodeToString([]byte(testValue))
		
		options := []string{
			"what=" + encoded,
			"quiet=true",
		}
		
		opts, _, err := DeserializeOptions(options)
		testutil.AssertNoError(t, err)
		
		testutil.AssertEqual(t, testValue, opts["what"])
		testutil.AssertEqual(t, "true", opts["quiet"])
	})

	t.Run("deserialize invalid format", func(t *testing.T) {
		options := []string{
			"invalidformat", // No equals sign
		}
		
		_, _, err := DeserializeOptions(options)
		testutil.AssertError(t, err, "Unable to parse options")
	})

	t.Run("deserialize invalid base64", func(t *testing.T) {
		options := []string{
			"what=base64%invalid!!!base64",
		}
		
		_, _, err := DeserializeOptions(options)
		testutil.AssertError(t, err, "")
	})

	t.Run("deserialize invalid numeric values", func(t *testing.T) {
		options := []string{
			"before=notanumber",
		}
		
		_, _, err := DeserializeOptions(options)
		testutil.AssertError(t, err, "")
	})

	t.Run("string representation", func(t *testing.T) {
		args := Args{
			Quiet:      true,
			Plain:      true,
			ServersStr: "server1,server2",
			What:       "error",
			UserName:   "testuser",
			SSHPort:    2222,
		}
		
		str := args.String()
		testutil.AssertContains(t, str, "Quiet:true")
		testutil.AssertContains(t, str, "Plain:true")
		testutil.AssertContains(t, str, "ServersStr:server1,server2")
		testutil.AssertContains(t, str, "What:error")
		testutil.AssertContains(t, str, "UserName:testuser")
		testutil.AssertContains(t, str, "SSHPort:2222")
	})
}