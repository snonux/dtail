// Package version provides version information and display utilities for DTail.
// It manages the application version, protocol compatibility version, and
// provides both plain and color-formatted version output for user interfaces.
//
// The version system includes:
// - Application version for release tracking
// - Protocol compatibility version for client-server communication
// - Color-formatted output for enhanced user experience
// - Exit utilities for command-line version display
//
// Version compatibility is critical for DTail's distributed architecture
// to ensure clients and servers can communicate properly across different
// software versions.
package version

import (
	"fmt"
	"os"

	"github.com/mimecast/dtail/internal/color"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/protocol"
)

const (
	// Name of DTail.
	Name string = "DTail"
	// Version of DTail.
	Version string = "4.4.0-develop"
	// Additional information for DTail
	Additional string = "Have a lot of fun!"
)

// String returns a plain text representation of the DTail version information
// including application name, version number, protocol compatibility version,
// and additional information. This format is suitable for logging and
// non-terminal output.
func String() string {
	return fmt.Sprintf("%s %v Protocol %s %s", Name, Version,
		protocol.ProtocolCompat, Additional)
}

// PaintedString returns a color-formatted version string with enhanced visual
// presentation using ANSI color codes. Each component (name, version, protocol,
// additional info) is displayed with different colors and attributes for
// better readability in terminal environments.
func PaintedString() string {
	if !config.Client.TermColorsEnable {
		return String()
	}

	name := color.PaintStrWithAttr(fmt.Sprintf(" %s ", Name),
		color.FgYellow, color.BgBlue, color.AttrBold)
	version := color.PaintStrWithAttr(fmt.Sprintf(" %s ", Version),
		color.FgBlue, color.BgYellow, color.AttrBold)
	protocol := color.PaintStr(fmt.Sprintf(" Protocol %s ", protocol.ProtocolCompat),
		color.FgBlack, color.BgGreen)
	additional := color.PaintStrWithAttr(fmt.Sprintf(" %s ", Additional),
		color.FgWhite, color.BgMagenta, color.AttrUnderline)

	return fmt.Sprintf("%s%v%s%s", name, version, protocol, additional)
}

// Print the version.
func Print() {
	fmt.Println(PaintedString())
}

// PrintAndExit prints the program version and exists.
func PrintAndExit() {
	Print()
	os.Exit(0)
}
