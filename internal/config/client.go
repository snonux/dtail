package config

import "github.com/mimecast/dtail/internal/color"

type remoteTermColors struct {
	DelimiterAttr color.Attribute
	DelimiterBg   color.BgColor
	DelimiterFg   color.FgColor
	RemoteAttr    color.Attribute
	RemoteBg      color.BgColor
	RemoteFg      color.FgColor
	CountAttr     color.Attribute
	CountBg       color.BgColor
	CountFg       color.FgColor
	HostnameAttr  color.Attribute
	HostnameBg    color.BgColor
	HostnameFg    color.FgColor
	IdAttr        color.Attribute
	IdBg          color.BgColor
	IdFg          color.FgColor
	StatsOkAttr   color.Attribute
	StatsOkBg     color.BgColor
	StatsOkFg     color.FgColor
	StatsWarnAttr color.Attribute
	StatsWarnBg   color.BgColor
	StatsWarnFg   color.FgColor
	TextAttr      color.Attribute
	TextBg        color.BgColor
	TextFg        color.FgColor
}

type clientTermColors struct {
	DelimiterAttr color.Attribute
	DelimiterBg   color.BgColor
	DelimiterFg   color.FgColor
	ClientAttr    color.Attribute
	ClientBg      color.BgColor
	ClientFg      color.FgColor
	HostnameAttr  color.Attribute
	HostnameBg    color.BgColor
	HostnameFg    color.FgColor
	TextAttr      color.Attribute
	TextBg        color.BgColor
	TextFg        color.FgColor
}

type serverTermColors struct {
	DelimiterAttr color.Attribute
	DelimiterBg   color.BgColor
	DelimiterFg   color.FgColor
	ServerAttr    color.Attribute
	ServerBg      color.BgColor
	ServerFg      color.FgColor
	HostnameAttr  color.Attribute
	HostnameBg    color.BgColor
	HostnameFg    color.FgColor
	TextAttr      color.Attribute
	TextBg        color.BgColor
	TextFg        color.FgColor
}

type commonTermColors struct {
	SeverityErrorAttr color.Attribute
	SeverityErrorBg   color.BgColor
	SeverityErrorFg   color.FgColor
	SeverityFatalAttr color.Attribute
	SeverityFatalBg   color.BgColor
	SeverityFatalFg   color.FgColor
	SeverityWarnAttr  color.Attribute
	SeverityWarnBg    color.BgColor
	SeverityWarnFg    color.FgColor
}

type termColors struct {
	Remote remoteTermColors
	Client clientTermColors
	Server serverTermColors
	Common commonTermColors
}

// ClientConfig represents a DTail client configuration (empty as of now as there
// are no available config options yet, but that may changes in the future).
type ClientConfig struct {
	TermColorsEnable bool
	TermColors       termColors `json:",omitempty"`
}

// Create a new default client configuration.
func newDefaultClientConfig() *ClientConfig {
	return &ClientConfig{
		TermColorsEnable: true,
		TermColors: termColors{
			Remote: remoteTermColors{
				DelimiterAttr: color.AttrDim,
				DelimiterBg:   color.BgBlue,
				DelimiterFg:   color.FgCyan,
				RemoteAttr:    color.AttrDim,
				RemoteBg:      color.BgBlue,
				RemoteFg:      color.FgWhite,
				CountAttr:     color.AttrDim,
				CountBg:       color.BgBlue,
				CountFg:       color.FgGreen,
				HostnameAttr:  color.AttrBold,
				HostnameBg:    color.BgBlue,
				HostnameFg:    color.FgWhite,
				IdAttr:        color.AttrDim,
				IdBg:          color.BgBlue,
				IdFg:          color.FgWhite,
				StatsOkAttr:   color.AttrNone,
				StatsOkBg:     color.BgGreen,
				StatsOkFg:     color.FgBlue,
				StatsWarnAttr: color.AttrNone,
				StatsWarnBg:   color.BgRed,
				StatsWarnFg:   color.FgWhite,
				TextAttr:      color.AttrNone,
				TextBg:        color.BgBlack,
				TextFg:        color.FgWhite,
			},
			Client: clientTermColors{
				DelimiterAttr: color.AttrDim,
				DelimiterBg:   color.BgYellow,
				DelimiterFg:   color.FgBlack,
				ClientAttr:    color.AttrDim,
				ClientBg:      color.BgYellow,
				ClientFg:      color.FgBlack,
				HostnameAttr:  color.AttrDim,
				HostnameBg:    color.BgYellow,
				HostnameFg:    color.FgBlack,
				TextAttr:      color.AttrNone,
				TextBg:        color.BgBlack,
				TextFg:        color.FgWhite,
			},
			Server: serverTermColors{
				DelimiterAttr: color.AttrDim,
				DelimiterBg:   color.BgCyan,
				DelimiterFg:   color.FgBlack,
				ServerAttr:    color.AttrDim,
				ServerBg:      color.BgCyan,
				ServerFg:      color.FgBlack,
				HostnameAttr:  color.AttrBold,
				HostnameBg:    color.BgCyan,
				HostnameFg:    color.FgBlack,
				TextAttr:      color.AttrNone,
				TextBg:        color.BgCyan,
				TextFg:        color.FgBlack,
			},
			Common: commonTermColors{
				SeverityErrorAttr: color.AttrBold,
				SeverityErrorBg:   color.BgRed,
				SeverityErrorFg:   color.FgWhite,
				SeverityFatalAttr: color.AttrBlink,
				SeverityFatalBg:   color.BgRed,
				SeverityFatalFg:   color.FgWhite,
				SeverityWarnAttr:  color.AttrBold,
				SeverityWarnBg:    color.BgBlack,
				SeverityWarnFg:    color.FgWhite,
			},
		},
	}
}
