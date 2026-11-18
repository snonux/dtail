package logformat

import (
	"slices"
	"strings"
)

// probeCollector builds a ProbeResult within the Probe* caps. Its zero value
// is ready to use; table names, keys and values are copied when they are
// added, so they may borrow a sampled line, and the result does not keep a
// large sampled line alive.
type probeCollector struct {
	result ProbeResult
}

// addFields adds the fields of one parsed line of table: $-variables to
// Variables, the "*" placeholder nowhere, every other field to the table.
func (c *probeCollector) addFields(table string, fields map[string]string) {
	tableFields := c.ensureTable(table)
	for key, value := range fields {
		switch {
		case key == "*":
		case strings.HasPrefix(key, "$"):
			c.addVariableValues(key, []string{value})
		default:
			if tableFields != nil {
				addProbeValues(tableFields, key, []string{value})
			}
		}
	}
}

// ensureTable adds table without fields unless it exists, and returns its
// fields. It returns nil when the table is new and the result already holds
// ProbeMaxTables tables. The name is cut to ProbeMaxNameBytes first.
func (c *probeCollector) ensureTable(table string) map[string][]string {
	table = truncateUTF8(table, ProbeMaxNameBytes)
	if c.result.Tables == nil {
		c.result.Tables = make(map[string]map[string][]string)
	}
	fields, ok := c.result.Tables[table]
	if !ok {
		if len(c.result.Tables) >= ProbeMaxTables {
			return nil
		}
		fields = make(map[string][]string)
		c.result.Tables[strings.Clone(table)] = fields
	}
	return fields
}

// ensureField adds field to table without values, unless it exists or the
// table is full. It lets a probe list fields it saw no value of, e.g. the
// columns of a csv header row.
func (c *probeCollector) ensureField(table, field string) {
	c.addValues(table, field, nil)
}

// addValues adds the values to field of table, within the caps.
func (c *probeCollector) addValues(table, field string, values []string) {
	if fields := c.ensureTable(table); fields != nil {
		addProbeValues(fields, field, values)
	}
}

// ensureVariable adds the $-variable name without values unless it exists or
// Variables is full.
func (c *probeCollector) ensureVariable(name string) {
	c.addVariableValues(name, nil)
}

// addVariableValues adds the values to the $-variable name, within the caps.
func (c *probeCollector) addVariableValues(name string, values []string) {
	if c.result.Variables == nil {
		c.result.Variables = make(map[string][]string)
	}
	addProbeValues(c.result.Variables, name, values)
}

// addProbeValues adds key, cut to ProbeMaxNameBytes, to fields unless fields
// already holds ProbeMaxFieldsPerTable keys, and then the values not seen yet
// (compared after the cut to ProbeMaxValueBytes) up to ProbeMaxValuesPerField.
// Keys that are equal after the cut share one entry.
func addProbeValues(fields map[string][]string, key string, values []string) {
	key = truncateUTF8(key, ProbeMaxNameBytes)
	known, ok := fields[key]
	if !ok {
		if len(fields) >= ProbeMaxFieldsPerTable {
			return
		}
		key = strings.Clone(key)
		known = []string{}
	}
	for _, value := range values {
		if len(known) >= ProbeMaxValuesPerField {
			break
		}
		value = truncateUTF8(value, ProbeMaxValueBytes)
		if !slices.Contains(known, value) {
			known = append(known, strings.Clone(value))
		}
	}
	fields[key] = known
}
