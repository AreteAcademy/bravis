package load

import (
	"fmt"
	"sort"
	"strings"

	"cloud.google.com/go/bigquery"
)

// reconcile decides which columns the MERGE inserts, and refuses the cases
// where inserting would lose or misplace data.
//
// The destination schema is the authority: it is the one that has to be
// satisfied, and it fixes the order. The values come from the staging table.
//
// The rule is asymmetric on purpose:
//
//   - a column in incoming that the destination lacks is an error naming it.
//     Proceeding would drop that data with no signal, which is the worst way
//     to fail: it disappears and nothing says so.
//   - a column in the destination that incoming lacks is fine. It stays NULL,
//     which a landing table legitimately does -- source_key is nullable and a
//     payload need not fill it.
//   - the same name with incompatible types is an error naming the column and
//     both types. That is today's positional failure, said in the language of
//     whoever has to fix it.
//
// Returns the intersection, in destination order.
func reconcile(dest, incoming bigquery.Schema) ([]string, error) {
	destTypes := make(map[string]bigquery.FieldType, len(dest))
	for _, f := range dest {
		destTypes[f.Name] = f.Type
	}

	var extra, mismatched []string
	incomingTypes := make(map[string]bigquery.FieldType, len(incoming))
	for _, f := range incoming {
		incomingTypes[f.Name] = f.Type
		destType, present := destTypes[f.Name]
		if !present {
			extra = append(extra, f.Name)
			continue
		}
		if !compatible(destType, f.Type) {
			mismatched = append(mismatched, fmt.Sprintf("%s (destination %s, incoming %s)",
				f.Name, destType, f.Type))
		}
	}

	if len(extra) > 0 {
		sort.Strings(extra)
		return nil, fmt.Errorf("the rows carry column(s) %s, which %s does not have. "+
			"They would be silently dropped, so the load stops here: add the column to the "+
			"table, or remove the field in Transform", strings.Join(extra, ", "), namesOf(dest))
	}
	if len(mismatched) > 0 {
		sort.Strings(mismatched)
		return nil, fmt.Errorf("type mismatch on %s", strings.Join(mismatched, "; "))
	}

	cols := make([]string, 0, len(dest))
	for _, f := range dest {
		if _, present := incomingTypes[f.Name]; present {
			cols = append(cols, f.Name)
		}
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("no column in common between the rows and the destination")
	}

	return cols, nil
}

// compatible reports whether a value of the incoming type can land in a
// column of the destination type.
//
// Only exact matches, plus the widening BigQuery does on its own for numbers:
// an INTEGER column accepts an INTEGER, and a FLOAT column accepts an INTEGER
// too. Anything looser here would recreate, in Go, the silent coercion this
// function exists to prevent.
func compatible(dest, incoming bigquery.FieldType) bool {
	if dest == incoming {
		return true
	}
	return dest == bigquery.FloatFieldType && incoming == bigquery.IntegerFieldType
}

func namesOf(s bigquery.Schema) string {
	if len(s) == 0 {
		return "the destination"
	}
	names := make([]string, 0, len(s))
	for _, f := range s {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return "the destination (" + strings.Join(names, ", ") + ")"
}

// mergeSQL builds the MERGE.
//
// Pure on purpose: the statement used to be assembled inside a method that
// needs a BigQuery client, which is why no unit test had ever seen the string
// it produced.
//
// Every identifier is quoted. Not fussiness: full, range and comment are
// reserved words in BigQuery and do appear as column names in real consumer
// data. An unquoted column called range turns this into a syntax error that
// only shows up at the one client who has it.
func mergeSQL(dest, temp *bigquery.Table, cols []string, key string) string {
	names := make([]string, len(cols))
	values := make([]string, len(cols))
	for i, c := range cols {
		names[i] = quote(c)
		values[i] = "incoming." + quote(c)
	}

	// WHEN NOT MATCHED only. A row already there is left exactly as it was:
	// a re-run must skip history, never rewrite it.
	return fmt.Sprintf(
		"MERGE %s AS target\n"+
			"USING %s AS incoming\n"+
			"ON target.%s = incoming.%s\n"+
			"WHEN NOT MATCHED THEN\n"+
			"  INSERT (%s)\n"+
			"  VALUES (%s)",
		tableRef(dest), tableRef(temp), quote(key), quote(key),
		strings.Join(names, ", "), strings.Join(values, ", "))
}

func tableRef(t *bigquery.Table) string {
	return fmt.Sprintf("`%s.%s.%s`", t.ProjectID, t.DatasetID, t.TableID)
}

// quote wraps an identifier in backticks, escaping any backtick inside it so
// a crafted column name cannot end the quoting early.
func quote(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "\\`") + "`"
}
