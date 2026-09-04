package load

import (
	"fmt"
	"sort"
	"strings"

	"cloud.google.com/go/bigquery"
)

// checkDeclaredAgainstTable confirms the declaration describes the table that
// is actually there.
//
// Asymmetric, the same way reconcile is and for the same reason: a declared
// column the table lacks is a load that cannot work, while a table column the
// declaration omits stays NULL, which a landing table legitimately does.
func checkDeclaredAgainstTable(declared []string, schema bigquery.Schema, table string) error {
	if len(declared) == 0 {
		return nil
	}

	has := make(map[string]bool, len(schema))
	for _, f := range schema {
		has[f.Name] = true
	}

	var absent []string
	for _, c := range declared {
		if !has[c] {
			absent = append(absent, c)
		}
	}
	if len(absent) == 0 {
		return nil
	}

	sort.Strings(absent)
	return fmt.Errorf("the Columns declaration lists %s, which %s does not have. The table has: %s",
		strings.Join(absent, ", "), table, namesOf(schema))
}
