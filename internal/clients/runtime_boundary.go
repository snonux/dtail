package clients

import (
	"fmt"
	"strings"
	"time"

	"github.com/mimecast/dtail/internal/color"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/mapr"
	serverHandlers "github.com/mimecast/dtail/internal/server/handlers"
	sshserver "github.com/mimecast/dtail/internal/ssh/server"
	user "github.com/mimecast/dtail/internal/user/server"
)

type clientRuntimeBoundary struct {
	sshPort           int
	sshConnectTimeout time.Duration
	interruptPause    time.Duration
	serverCfg         *config.ServerConfig
	output            *clientOutputFormatter
}

func newClientRuntimeBoundary(cfg config.RuntimeConfig) *clientRuntimeBoundary {
	sshPort := 2222
	sshConnectTimeout := 2 * time.Second
	if cfg.Common != nil {
		if cfg.Common.SSHPort > 0 {
			sshPort = cfg.Common.SSHPort
		}
		if cfg.Common.SSHConnectTimeoutMs > 0 {
			sshConnectTimeout = time.Duration(cfg.Common.SSHConnectTimeoutMs) * time.Millisecond
		}
	}

	return &clientRuntimeBoundary{
		sshPort:           sshPort,
		sshConnectTimeout: sshConnectTimeout,
		interruptPause:    time.Second * time.Duration(config.InterruptTimeoutS),
		serverCfg:         cfg.Server,
		output:            newClientOutputFormatter(cfg.Client),
	}
}

func (r *clientRuntimeBoundary) SSHPort() int {
	return r.sshPort
}

func (r *clientRuntimeBoundary) SSHConnectTimeout() time.Duration {
	return r.sshConnectTimeout
}

func (r *clientRuntimeBoundary) InterruptPause() time.Duration {
	if r == nil || r.interruptPause <= 0 {
		return time.Second * time.Duration(config.InterruptTimeoutS)
	}
	return r.interruptPause
}

func (r *clientRuntimeBoundary) NewServerlessHandler(userName string) (serverHandlers.Handler, error) {
	var permissionLookup user.PermissionLookup
	if r.serverCfg != nil {
		permissionLookup = r.serverCfg.UserPermissions
	}

	serverUser, err := user.New(userName, "local(serverless)", permissionLookup)
	if err != nil {
		return nil, err
	}

	switch userName {
	case config.HealthUser:
		return serverHandlers.NewHealthHandler(serverUser), nil
	default:
		if r.serverCfg == nil {
			return nil, fmt.Errorf("missing serverless server config")
		}
		// Serverless mode does not share an SSH server's auth-key store, so
		// create a dedicated store using the server config's TTL and max-key
		// limits. This avoids any package-level mutable state.
		keyStore := sshserver.NewAuthKeyStore(
			time.Duration(r.serverCfg.AuthKeyTTLSeconds)*time.Second,
			r.serverCfg.AuthKeyMaxPerUser,
		)
		return serverHandlers.NewServerHandler(
			serverUser,
			make(chan struct{}, positiveOrDefault(r.serverCfg.MaxConcurrentCats, 2)),
			make(chan struct{}, positiveOrDefault(r.serverCfg.MaxConcurrentTails, 50)),
			r.serverCfg,
			keyStore,
		), nil
	}
}

func positiveOrDefault(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

type interruptMessageFormatter interface {
	FormatInterruptMessage(index int, message string) string
}

type clientOutputFormatter struct {
	interruptEnabled bool
	interruptStyle   textStyle
	rawQueryEnabled  bool
	rawQueryStyle    textStyle
	maprRenderer     mapr.ResultRenderer
}

func newClientOutputFormatter(clientCfg *config.ClientConfig) *clientOutputFormatter {
	formatter := &clientOutputFormatter{
		maprRenderer: mapr.PlainResultRenderer(),
	}
	if clientCfg == nil || !clientCfg.TermColorsEnable {
		return formatter
	}

	formatter.interruptEnabled = true
	formatter.rawQueryEnabled = true
	formatter.interruptStyle = textStyle{
		fg:   clientCfg.TermColors.Client.ClientFg,
		bg:   clientCfg.TermColors.Client.ClientBg,
		attr: clientCfg.TermColors.Client.ClientAttr,
	}
	formatter.rawQueryStyle = textStyle{
		fg:   clientCfg.TermColors.MaprTable.RawQueryFg,
		bg:   clientCfg.TermColors.MaprTable.RawQueryBg,
		attr: clientCfg.TermColors.MaprTable.RawQueryAttr,
	}
	formatter.maprRenderer = maprTerminalRenderer{
		header: textStyle{
			fg:   clientCfg.TermColors.MaprTable.HeaderFg,
			bg:   clientCfg.TermColors.MaprTable.HeaderBg,
			attr: clientCfg.TermColors.MaprTable.HeaderAttr,
		},
		headerDelimiter: textStyle{
			fg:   clientCfg.TermColors.MaprTable.HeaderDelimiterFg,
			bg:   clientCfg.TermColors.MaprTable.HeaderDelimiterBg,
			attr: clientCfg.TermColors.MaprTable.HeaderDelimiterAttr,
		},
		headerSortAttr:  clientCfg.TermColors.MaprTable.HeaderSortKeyAttr,
		headerGroupAttr: clientCfg.TermColors.MaprTable.HeaderGroupKeyAttr,
		data: textStyle{
			fg:   clientCfg.TermColors.MaprTable.DataFg,
			bg:   clientCfg.TermColors.MaprTable.DataBg,
			attr: clientCfg.TermColors.MaprTable.DataAttr,
		},
		dataDelimiter: textStyle{
			fg:   clientCfg.TermColors.MaprTable.DelimiterFg,
			bg:   clientCfg.TermColors.MaprTable.DelimiterBg,
			attr: clientCfg.TermColors.MaprTable.DelimiterAttr,
		},
	}

	return formatter
}

func (f *clientOutputFormatter) FormatInterruptMessage(index int, message string) string {
	if index > 0 && f.interruptEnabled {
		return color.PaintStrWithAttr(message, f.interruptStyle.fg, f.interruptStyle.bg, f.interruptStyle.attr)
	}
	return " " + message
}

func (f *clientOutputFormatter) PaintMaprRawQuery(rawQuery string) string {
	if !f.rawQueryEnabled {
		return rawQuery
	}
	return color.PaintStrWithAttr(rawQuery, f.rawQueryStyle.fg, f.rawQueryStyle.bg, f.rawQueryStyle.attr)
}

func (f *clientOutputFormatter) MaprResultRenderer() mapr.ResultRenderer {
	if f == nil || f.maprRenderer == nil {
		return mapr.PlainResultRenderer()
	}
	return f.maprRenderer
}

type textStyle struct {
	fg   color.FgColor
	bg   color.BgColor
	attr color.Attribute
}

type maprTerminalRenderer struct {
	header          textStyle
	headerDelimiter textStyle
	headerSortAttr  color.Attribute
	headerGroupAttr color.Attribute
	data            textStyle
	dataDelimiter   textStyle
}

func (r maprTerminalRenderer) WriteHeaderEntry(sb *strings.Builder, text string, isSortKey, isGroupKey bool) {
	attrs := []color.Attribute{r.header.attr}
	if isSortKey {
		attrs = append(attrs, r.headerSortAttr)
	}
	if isGroupKey {
		attrs = append(attrs, r.headerGroupAttr)
	}
	color.PaintWithAttrs(sb, text, r.header.fg, r.header.bg, attrs)
}

func (r maprTerminalRenderer) WriteHeaderDelimiter(sb *strings.Builder, text string) {
	color.PaintWithAttr(sb, text, r.headerDelimiter.fg, r.headerDelimiter.bg, r.headerDelimiter.attr)
}

func (r maprTerminalRenderer) WriteDataEntry(sb *strings.Builder, text string) {
	color.PaintWithAttr(sb, text, r.data.fg, r.data.bg, r.data.attr)
}

func (r maprTerminalRenderer) WriteDataDelimiter(sb *strings.Builder, text string) {
	color.PaintWithAttr(sb, text, r.dataDelimiter.fg, r.dataDelimiter.bg, r.dataDelimiter.attr)
}
