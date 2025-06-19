// CLAUDE: Refactor this package into the constants package. Rename all constats moved to the constants package so that they all have a suffix Protocol.... e.g. MessageDelimiter => ProtocolMessageDelimiter, and so on.
// Package protocol defines the communication protocol constants used throughout
// the DTail distributed log processing system. This package contains the
// delimiter characters and protocol version that enable proper message parsing
// and serialization between DTail clients and servers.
//
// The protocol uses specific Unicode characters as delimiters to avoid conflicts
// with typical log file content while maintaining human readability during debugging.
package protocol

const (
	// ProtocolCompat defines the compatibility version string used to ensure
	// client-server protocol compatibility. Both client and server must have
	// matching protocol versions to communicate successfully.
	ProtocolCompat string = "4.1"

	// MessageDelimiter is the byte used to separate individual protocol messages
	// in the communication stream. Uses the Unicode "not sign" (¬) character to
	// minimize conflicts with log file content.
	MessageDelimiter byte = '¬'

	// FieldDelimiter separates fields within a single protocol message.
	// Uses the pipe character (|) for field separation within structured messages.
	FieldDelimiter string = "|"

	// CSVDelimiter is used when outputting results in CSV format, providing
	// standard comma-separated value formatting for tabular data export.
	CSVDelimiter string = ","

	// AggregateKVDelimiter separates key-value pairs within MapReduce aggregation
	// messages. Uses the Unicode "colon equals" (≔) character for clear separation
	// of keys from their corresponding values in aggregation results.
	AggregateKVDelimiter string = "≔"

	// AggregateDelimiter separates different sections of aggregation messages,
	// such as separating metadata from data or different aggregation groups.
	// Uses the Unicode "double vertical line" (∥) character.
	AggregateDelimiter string = "∥"

	// AggregateGroupKeyCombinator combines multiple group key fields into a
	// single composite key when performing GROUP BY operations in MapReduce queries.
	// Uses comma separation for multi-field grouping keys.
	AggregateGroupKeyCombinator string = ","
)
