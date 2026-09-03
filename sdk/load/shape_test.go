package load

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	core "github.com/AreteAcademy/bravis/sdk/internal/core"
)

func TestEnvelopeMatchesZarvShape(t *testing.T) {
	l := &Loader{cfg: &core.LoadConfig{Format: "ndjson", WriteEnvelopeColumns: true}}
	data, err := l.encodeRows([]core.Envelope{{
		Provider: "open_meteo", Entity: "hourly", SourceKey: "k1",
		RecordTS: "2026-01-01T00:00:00Z", Payload: map[string]any{"temp": 20},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &row); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(row))
	for k := range row {
		got = append(got, k)
	}
	sort.Strings(got)
	want := "entity,ingestion_id,ingestion_loaded_at,payload,provider,source_key"
	if strings.Join(got, ",") != want {
		t.Errorf("shape = %s\nwant  = %s", strings.Join(got, ","), want)
	}
}
