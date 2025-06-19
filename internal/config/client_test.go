package config

import (
	"testing"

	"github.com/mimecast/dtail/internal/color"
	"github.com/mimecast/dtail/internal/testutil"
)

func TestClientConfig(t *testing.T) {
	t.Run("default values", func(t *testing.T) {
		c := ClientConfig{}
		
		// Test default values
		testutil.AssertEqual(t, false, c.TermColorsEnable)
		
		// Test that color structs are zero-valued by default
		testutil.AssertEqual(t, color.Attribute(""), c.TermColors.Remote.RemoteAttr)
		testutil.AssertEqual(t, color.BgColor(""), c.TermColors.Remote.RemoteBg)
		testutil.AssertEqual(t, color.FgColor(""), c.TermColors.Remote.RemoteFg)
	})

	t.Run("default client config", func(t *testing.T) {
		c := newDefaultClientConfig()
		
		// Should enable colors by default
		testutil.AssertEqual(t, true, c.TermColorsEnable)
		
		// Test some default color settings
		testutil.AssertEqual(t, color.AttrDim, c.TermColors.Remote.DelimiterAttr)
		testutil.AssertEqual(t, color.BgBlue, c.TermColors.Remote.DelimiterBg)
		testutil.AssertEqual(t, color.FgCyan, c.TermColors.Remote.DelimiterFg)
		
		testutil.AssertEqual(t, color.AttrDim, c.TermColors.Client.ClientAttr)
		testutil.AssertEqual(t, color.BgYellow, c.TermColors.Client.ClientBg)
		testutil.AssertEqual(t, color.FgBlack, c.TermColors.Client.ClientFg)
		
		testutil.AssertEqual(t, color.AttrBold, c.TermColors.Common.SeverityErrorAttr)
		testutil.AssertEqual(t, color.BgRed, c.TermColors.Common.SeverityErrorBg)
		testutil.AssertEqual(t, color.FgWhite, c.TermColors.Common.SeverityErrorFg)
	})

	t.Run("remote term colors", func(t *testing.T) {
		c := ClientConfig{
			TermColorsEnable: true,
			TermColors: termColors{
				Remote: remoteTermColors{
					RemoteAttr: color.AttrBold,
					RemoteBg:   color.BgBlack,
					RemoteFg:   color.FgWhite,
					HostnameAttr: color.AttrUnderline,
					HostnameBg:   color.BgGreen,
					HostnameFg:   color.FgBlack,
				},
			},
		}
		
		testutil.AssertEqual(t, color.AttrBold, c.TermColors.Remote.RemoteAttr)
		testutil.AssertEqual(t, color.BgBlack, c.TermColors.Remote.RemoteBg)
		testutil.AssertEqual(t, color.FgWhite, c.TermColors.Remote.RemoteFg)
		testutil.AssertEqual(t, color.AttrUnderline, c.TermColors.Remote.HostnameAttr)
		testutil.AssertEqual(t, color.BgGreen, c.TermColors.Remote.HostnameBg)
		testutil.AssertEqual(t, color.FgBlack, c.TermColors.Remote.HostnameFg)
	})

	t.Run("severity colors", func(t *testing.T) {
		c := ClientConfig{
			TermColors: termColors{
				Common: commonTermColors{
					SeverityErrorAttr: color.AttrBold,
					SeverityErrorBg:   color.BgRed,
					SeverityErrorFg:   color.FgWhite,
					SeverityFatalAttr: color.AttrBlink,
					SeverityFatalBg:   color.BgMagenta,
					SeverityFatalFg:   color.FgYellow,
					SeverityWarnAttr:  color.AttrDim,
					SeverityWarnBg:    color.BgYellow,
					SeverityWarnFg:    color.FgBlack,
				},
			},
		}
		
		// Test error colors
		testutil.AssertEqual(t, color.AttrBold, c.TermColors.Common.SeverityErrorAttr)
		testutil.AssertEqual(t, color.BgRed, c.TermColors.Common.SeverityErrorBg)
		testutil.AssertEqual(t, color.FgWhite, c.TermColors.Common.SeverityErrorFg)
		
		// Test fatal colors
		testutil.AssertEqual(t, color.AttrBlink, c.TermColors.Common.SeverityFatalAttr)
		testutil.AssertEqual(t, color.BgMagenta, c.TermColors.Common.SeverityFatalBg)
		testutil.AssertEqual(t, color.FgYellow, c.TermColors.Common.SeverityFatalFg)
		
		// Test warn colors
		testutil.AssertEqual(t, color.AttrDim, c.TermColors.Common.SeverityWarnAttr)
		testutil.AssertEqual(t, color.BgYellow, c.TermColors.Common.SeverityWarnBg)
		testutil.AssertEqual(t, color.FgBlack, c.TermColors.Common.SeverityWarnFg)
	})

	t.Run("mapr table colors", func(t *testing.T) {
		c := ClientConfig{
			TermColors: termColors{
				MaprTable: maprTableTermColors{
					HeaderAttr:         color.AttrBold,
					HeaderBg:           color.BgBlue,
					HeaderFg:           color.FgWhite,
					HeaderSortKeyAttr:  color.AttrUnderline,
					HeaderGroupKeyAttr: color.AttrReverse,
				},
			},
		}
		
		testutil.AssertEqual(t, color.AttrBold, c.TermColors.MaprTable.HeaderAttr)
		testutil.AssertEqual(t, color.BgBlue, c.TermColors.MaprTable.HeaderBg)
		testutil.AssertEqual(t, color.FgWhite, c.TermColors.MaprTable.HeaderFg)
		testutil.AssertEqual(t, color.AttrUnderline, c.TermColors.MaprTable.HeaderSortKeyAttr)
		testutil.AssertEqual(t, color.AttrReverse, c.TermColors.MaprTable.HeaderGroupKeyAttr)
	})
}