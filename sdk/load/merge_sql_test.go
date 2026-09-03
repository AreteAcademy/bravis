package load

import (
	"strings"
	"testing"

	"cloud.google.com/go/bigquery"
)

func schema(pairs ...any) bigquery.Schema {
	var s bigquery.Schema
	for i := 0; i < len(pairs); i += 2 {
		s = append(s, &bigquery.FieldSchema{
			Name: pairs[i].(string),
			Type: pairs[i+1].(bigquery.FieldType),
		})
	}
	return s
}

const (
	str = bigquery.StringFieldType
	i64 = bigquery.IntegerFieldType
	f64 = bigquery.FloatFieldType
	ts  = bigquery.TimestampFieldType
)

func tbl(id string) *bigquery.Table {
	return &bigquery.Table{ProjectID: "p", DatasetID: "d", TableID: id}
}

// The defect this file exists for: the two tables carry the same columns in
// a different order. INSERT ROW would send the INT64 into the STRING column.
func TestMergeUsesDestinationOrderNotIncomingOrder(t *testing.T) {
	dest := schema("ingestion_id", str, "a", str, "b", i64)
	incoming := schema("b", i64, "ingestion_id", str, "a", str)

	cols, err := reconcile(dest, incoming)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	want := []string{"ingestion_id", "a", "b"}
	if strings.Join(cols, ",") != strings.Join(want, ",") {
		t.Fatalf("columns came back as %v, want destination order %v", cols, want)
	}

	sql := mergeSQL(tbl("dest"), tbl("tmp"), cols, "ingestion_id")

	// Each value has to be named against its own column, in the same order.
	if !strings.Contains(sql, "INSERT (`ingestion_id`, `a`, `b`)") {
		t.Errorf("column list is not in destination order:\n%s", sql)
	}
	if !strings.Contains(sql, "VALUES (incoming.`ingestion_id`, incoming.`a`, incoming.`b`)") {
		t.Errorf("value list does not line up with the column list:\n%s", sql)
	}
}

// INSERT ROW is positional. Nothing may generate it again.
func TestMergeNeverEmitsInsertRow(t *testing.T) {
	sql := mergeSQL(tbl("dest"), tbl("tmp"), []string{"ingestion_id"}, "ingestion_id")
	if strings.Contains(sql, "INSERT ROW") {
		t.Fatalf("INSERT ROW matches by position, not by name:\n%s", sql)
	}
}

// full, range and comment are reserved in BigQuery, and real payloads have
// them. Unquoted, this is a syntax error only the one client who has such a
// column ever sees.
func TestMergeQuotesReservedWords(t *testing.T) {
	cols := []string{"ingestion_id", "full", "range", "comment"}
	sql := mergeSQL(tbl("dest"), tbl("tmp"), cols, "ingestion_id")

	for _, c := range cols {
		if !strings.Contains(sql, "`"+c+"`") {
			t.Errorf("column %q is not backticked:\n%s", c, sql)
		}
	}
	if strings.Contains(sql, "incoming.full") || strings.Contains(sql, ", full,") {
		t.Errorf("a reserved word appears unquoted:\n%s", sql)
	}
}

func TestMergeQuotesTheKeyAndTheTables(t *testing.T) {
	sql := mergeSQL(tbl("dest"), tbl("tmp"), []string{"range"}, "range")

	if !strings.Contains(sql, "ON target.`range` = incoming.`range`") {
		t.Errorf("the join key is not quoted:\n%s", sql)
	}
	if !strings.Contains(sql, "`p.d.dest`") || !strings.Contains(sql, "`p.d.tmp`") {
		t.Errorf("a table reference is not quoted:\n%s", sql)
	}
}

// A column name is data, and data does not get to end the quoting.
func TestMergeEscapesABacktickInsideAName(t *testing.T) {
	sql := mergeSQL(tbl("dest"), tbl("tmp"), []string{"a`b"}, "id")
	if strings.Contains(sql, "`a`b`") {
		t.Fatalf("a backtick in the name escaped its quoting:\n%s", sql)
	}
	if !strings.Contains(sql, "`a\\`b`") {
		t.Fatalf("expected the backtick escaped, got:\n%s", sql)
	}
}

// Asymmetric on purpose, and this is the half that has to fail loudly.
func TestReconcileRefusesAColumnTheDestinationLacks(t *testing.T) {
	dest := schema("ingestion_id", str, "a", str)
	incoming := schema("ingestion_id", str, "a", str, "surpresa", i64)

	_, err := reconcile(dest, incoming)
	if err == nil {
		t.Fatal("an extra column would be dropped in silence; the load must stop")
	}
	if !strings.Contains(err.Error(), "surpresa") {
		t.Errorf("the error does not name the column: %v", err)
	}
}

// The other half: this one is legitimate, and must not fail.
func TestReconcileAllowsAColumnTheRowsOmit(t *testing.T) {
	dest := schema("ingestion_id", str, "a", str, "source_key", str)
	incoming := schema("ingestion_id", str, "a", str)

	cols, err := reconcile(dest, incoming)
	if err != nil {
		t.Fatalf("a nullable column the rows omit is normal: %v", err)
	}
	if strings.Join(cols, ",") != "ingestion_id,a" {
		t.Fatalf("got %v, want only the columns actually present", cols)
	}
}

func TestReconcileRefusesATypeMismatchAndNamesBothTypes(t *testing.T) {
	dest := schema("ingestion_id", str, "quando", ts)
	incoming := schema("ingestion_id", str, "quando", str)

	_, err := reconcile(dest, incoming)
	if err == nil {
		t.Fatal("a STRING into a TIMESTAMP column has to be refused")
	}
	for _, want := range []string{"quando", "TIMESTAMP", "STRING"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

// BigQuery widens an INT64 into a FLOAT64 column on its own; refusing it here
// would reject loads that work.
func TestReconcileAcceptsIntegerIntoFloat(t *testing.T) {
	if _, err := reconcile(schema("v", f64), schema("v", i64)); err != nil {
		t.Fatalf("INTEGER into FLOAT is a widening BigQuery does: %v", err)
	}
	if _, err := reconcile(schema("v", i64), schema("v", f64)); err == nil {
		t.Fatal("FLOAT into INTEGER loses the fraction and must be refused")
	}
}

func TestReconcileRefusesWhenNothingIsInCommon(t *testing.T) {
	if _, err := reconcile(schema("a", str), schema("b", str)); err == nil {
		t.Fatal("with no column in common the MERGE would insert nothing")
	}
}
