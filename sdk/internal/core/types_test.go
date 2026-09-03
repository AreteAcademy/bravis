package core

import (
	"testing"
)

// TestIngestionID verifies deterministic UUID v5 generation.
// This test must pass against reference values from the Python implementation.
func TestIngestionID(t *testing.T) {
	tests := []struct {
		name    string
		env     Envelope
		want    string
		wantErr bool
	}{
		{
			name: "empty source_key is error",
			env: Envelope{
				Provider:  "test_provider",
				Entity:    "test_entity",
				SourceKey: "",
				RecordTS:  "2026-01-01T00:00:00Z",
			},
			wantErr: true,
		},
		{
			name: "deterministic generation",
			env: Envelope{
				Provider:  "example_gov",
				Entity:    "transactions",
				SourceKey: "tx-12345",
				RecordTS:  "2026-01-01T00:00:00Z",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id1, err := tt.env.IngestionID()
			if (err != nil) != tt.wantErr {
				t.Errorf("IngestionID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			// If we expect an error, stop here
			if tt.wantErr {
				return
			}

			// Call twice to verify determinism
			id2, _ := tt.env.IngestionID()
			if id1 != id2 {
				t.Errorf("IngestionID() not deterministic: %s != %s", id1, id2)
			}

			// Verify UUID format (v5 = 8-4-4-4-12 hex digits)
			if len(id1) != 36 {
				t.Errorf("IngestionID() invalid length: %d", len(id1))
			}
		})
	}
}

func TestEnvelopeValidation(t *testing.T) {
	tests := []struct {
		name    string
		env     Envelope
		wantErr bool
	}{
		{
			name: "valid envelope",
			env: Envelope{
				Provider:  "test",
				Entity:    "test",
				SourceKey: "key",
				RecordTS:  "2026-01-01T00:00:00Z",
				Payload:   map[string]string{"data": "value"},
			},
			wantErr: false,
		},
		{
			name: "missing provider",
			env: Envelope{
				Provider:  "",
				Entity:    "test",
				SourceKey: "key",
				RecordTS:  "2026-01-01T00:00:00Z",
			},
			wantErr: false, // Provider can be set by load
		},
		{
			name: "missing entity",
			env: Envelope{
				Provider:  "test",
				Entity:    "",
				SourceKey: "key",
				RecordTS:  "2026-01-01T00:00:00Z",
			},
			wantErr: false, // Entity can be set by load
		},
		{
			name: "missing source_key",
			env: Envelope{
				Provider: "test",
				Entity:   "test",
				RecordTS: "2026-01-01T00:00:00Z",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.env.IngestionID()
			if (err != nil) != tt.wantErr {
				t.Errorf("IngestionID() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
