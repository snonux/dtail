package handlers

import (
	"bytes"
	"strconv"

	"github.com/mimecast/dtail/internal/protocol"
)

const defaultTransmittedPerc = "100"

func formatRemoteHeader(buf *bytes.Buffer, hostname, transmittedPerc string, lineNum uint64, sourceID string) {
	buf.WriteString("REMOTE")
	buf.WriteString(protocol.FieldDelimiter)
	buf.WriteString(hostname)
	buf.WriteString(protocol.FieldDelimiter)
	buf.WriteString(transmittedPerc)
	buf.WriteString(protocol.FieldDelimiter)
	buf.WriteString(strconv.FormatUint(lineNum, 10))
	buf.WriteString(protocol.FieldDelimiter)
	buf.WriteString(sourceID)
	buf.WriteString(protocol.FieldDelimiter)
}

func formatRemoteLine(buf *bytes.Buffer, hostname, transmittedPerc string, lineNum uint64, sourceID string, content []byte) {
	formatRemoteHeader(buf, hostname, transmittedPerc, lineNum, sourceID)
	buf.Write(content)
	buf.WriteByte(protocol.MessageDelimiter)
}

func formatServerMessage(buf *bytes.Buffer, hostname, message string, plain bool) {
	if !plain {
		buf.WriteString("SERVER")
		buf.WriteString(protocol.FieldDelimiter)
		buf.WriteString(hostname)
		buf.WriteString(protocol.FieldDelimiter)
	}
	buf.WriteString(message)
	buf.WriteByte(protocol.MessageDelimiter)
}
