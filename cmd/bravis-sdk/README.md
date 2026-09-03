# Bravis SDK CLI

> Este é o CLI do **SDK** — `extract`, `load` e o pipeline entre os dois. O
> binário do Bravis (o engine: `serve`, `scheduler`, `migrate`, `publish`) é
> outro, e vive em [`cmd/bravis`](../bravis-sdk/).

Command-line interface for Bravis SDK. Extract and load data without writing Go code.

## Installation

```bash
go install github.com/AreteAcademy/bravis/cmd/bravis-sdk@latest
```

Or from source:

```bash
cd cmd/bravis-sdk
go build -o bravis-sdk
./bravis-sdk --help
```

To develop the CLI against your local SDK changes, create a `go.work` at the
repo root (it is gitignored — `cmd/bravis/go.mod` must keep requiring the
published SDK, since `go install pkg@version` rejects modules with `replace`
directives):

```bash
go work init ./sdk ./cmd/bravis
```

## Commands

### extract

Extract data from HTTP endpoint.

```bash
bravis-sdk extract https://api.example.com/data.csv
bravis-sdk extract https://api.example.com/data.json --format json --output json
bravis-sdk extract https://api.example.com/data --retries 5 --timeout 60s
```

**Flags:**
- `-f, --format` — Format: csv, json, ndjson, xml (auto-detect if empty)
- `-t, --timeout` — Timeout per attempt (default: 30s)
- `--total-timeout` — Total timeout (default: 5m)
- `-r, --retries` — Max retries (default: 3)
- `-o, --output` — Output format: table or json (default: table)

### load

Load NDJSON data to BigQuery (reads from stdin).

```bash
cat data.ndjson | bravis-sdk load --project my-project --dataset landing --table raw_data
```

**Flags:**
- `-p, --project` — GCP project ID (required)
- `-d, --dataset` — BigQuery dataset (default: landing)
- `-t, --table` — BigQuery table (default: raw_data)
- `-m, --metadata` — Add `ingestion_id` and `ingestion_loaded_at` to each row

### run

Extract and load in one pipeline.

```bash
bravis-sdk run https://api.example.com/data.csv --project my-project
```

**Flags:**
- `-p, --project` — GCP project ID (required)
- `-d, --dataset` — BigQuery dataset (default: landing)
- `-t, --table` — BigQuery table (default: raw_data)
- `-m, --metadata` — Add `ingestion_id` and `ingestion_loaded_at` to each row
- `--dry-run` — Extract only, don't load

### version

Show version information.

## Piping Commands

Chain extract and load:

```bash
bravis-sdk extract https://api.example.com/data.csv --output json | \
  bravis-sdk load --project my-project --dataset landing --table raw_data
```

## Environment Variables

GCP Authentication:
```bash
export GOOGLE_APPLICATION_CREDENTIALS=/path/to/credentials.json
gcloud auth application-default login
```

Bravis Configuration:
```bash
export BRAVIS_PROJECT=my-project
export BRAVIS_DATASET=landing
export BRAVIS_TABLE=raw_data
```

## Examples

### Simple extract

```bash
bravis-sdk extract https://api.example.com/data.csv
```

### Extract to JSON

```bash
bravis-sdk extract https://api.example.com/data.csv --output json
```

### Full pipeline

```bash
bravis-sdk run https://api.example.com/data.csv \
  --project my-project \
  --dataset landing \
  --table raw_data \
  --metadata
```

### Test without loading

```bash
bravis-sdk run https://api.example.com/data.csv \
  --project my-project \
  --dry-run
```

## Performance Tips

- Use `--retries 5` for unreliable networks
- Increase `--timeout` for slow APIs
- Use `--output json` for piping
- Batch loads when possible
- Use `--dry-run` to test first

## Development

Build locally:

```bash
cd cmd/bravis-sdk
go build -o bravis-sdk
./bravis-sdk --help
```

Build for distribution:

```bash
# macOS
GOOS=darwin GOARCH=amd64 go build -o bravis-sdk-darwin-amd64

# Linux
GOOS=linux GOARCH=amd64 go build -o bravis-sdk-linux-amd64

# Windows
GOOS=windows GOARCH=amd64 go build -o bravis-sdk-windows-amd64.exe
```

## License

MIT — see [LICENSE](../../LICENSE).
