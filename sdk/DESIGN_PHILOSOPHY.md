# SDK Design Philosophy

## Principle: No Schema Opinions

The Bravis SDK extracts and loads data, but **does not impose a table schema**.

This is intentional and a core design decision.

## Why?

### Problem: Opinionated SDKs Don't Scale

An SDK that says "you must use this exact schema" fails when:
- You have multiple data shapes (some wide, some deep)
- You want different table structures for different use cases
- You're consolidating data from multiple sources
- You need custom partitioning or clustering strategies
- You want to evolve your schema without upgrading the SDK

### Solution: SDK Provides Tools, You Make Decisions

**The SDK:**
- ✅ Extracts data from HTTP endpoints (with retry, timeout, format handling)
- ✅ Transforms to `Envelope` (portable, intermediate format)
- ✅ Writes raw JSON payloads to BigQuery
- ✅ Optionally adds metadata **to the payload**
- ✅ Handles strategy selection (inline vs GCS)

**You:**
- ✅ Define the BigQuery table schema
- ✅ Decide if you want metadata in payload or as separate columns
- ✅ Control partitioning, clustering, TTL
- ✅ Extract fields from JSON as needed
- ✅ Version your schema independently from SDK versions

## Envelope: Portable Intermediate Format

```go
type Envelope struct {
    Provider  string  // data source (e.g., "example_api")
    Entity    string  // entity type (e.g., "transactions")
    SourceKey string  // unique key from source (e.g., "tx-123")
    RecordTS  string  // timestamp at source
    Payload   any     // the actual data (any JSON-serializable shape)
}
```

**Envelope is a contract, not a database row.**

It flows through:
1. Extract → produces Envelopes
2. Your code → transforms Envelopes
3. Load → writes Envelopes to BigQuery

You decide:
- What goes into `Payload`
- Whether to add metadata
- How to structure the final row in BigQuery

## Metadata: Optional, Not Opinionated

When `ExtraMetadata: true`, the SDK adds fields to the `Payload`:

```json
{
  // your original data
  "id": "tx-123",
  "amount": 100.50,
  "currency": "USD",
  
  // bravis metadata (injected into payload)
  "ingestion_id": "550e8400-e29b-41d4-a716-446655440000",
  "ingestion_loaded_at": "2026-01-02T15:30:45Z",
  "# provider/entity/source_key stay provenance; you add them if you want them"
  "entity": "transactions",
  "source_key": "tx-123",
  "record_ts": "2026-01-02T10:00:00Z"
}
```

**Why in the payload?**
- Works with any schema (just more JSON fields)
- No schema restrictions
- Survives schema evolution
- Self-documenting

**When to use it?**
- ✅ You need deterministic IDs for deduplication
- ✅ You want audit trail of load time
- ✅ You need to trace data lineage
- ❌ You don't: schema without metadata is equally valid

## Schema Examples

### Simple: Just Payload

```sql
CREATE TABLE landing.raw_data (
  payload JSON NOT NULL
);
```

Load:
```go
loader.Load(ctx, envelopes...)  // ExtraMetadata: false (default)
```

Query:
```sql
SELECT
  JSON_EXTRACT_SCALAR(payload, '$.id') as id,
  JSON_EXTRACT_SCALAR(payload, '$.amount') as amount,
  payload
FROM landing.raw_data;
```

### Rich: Payload + Metadata in JSON

```sql
CREATE TABLE landing.raw_data (
  payload JSON NOT NULL
);
```

Load:
```go
loader.Load(ctx, &sdk.LoadConfig{
  ExtraMetadata: true,
  // ...
})
```

Query:
```sql
SELECT
  JSON_EXTRACT_SCALAR(payload, '$.ingestion_id') as ingestion_id,
  JSON_EXTRACT_SCALAR(payload, '$.ingestion_loaded_at') as loaded_at,
  JSON_EXTRACT_SCALAR(payload, '$.id') as id,
  JSON_EXTRACT_SCALAR(payload, '$.amount') as amount
FROM landing.raw_data
ORDER BY JSON_EXTRACT(payload, '$.ingestion_loaded_at') DESC;
```

### Structured: Extract to Columns

```sql
CREATE TABLE landing.transactions (
  ingestion_id STRING NOT NULL,
  loaded_at TIMESTAMP NOT NULL,
  id STRING NOT NULL,
  amount FLOAT64,
  currency STRING,
  raw_payload JSON,
  CONSTRAINT unique_ingestion UNIQUE(ingestion_id)
)
PARTITION BY DATE(loaded_at)
CLUSTER BY ingestion_id;
```

Load:
```go
loader.Load(ctx, &sdk.LoadConfig{
  ExtraMetadata: true,
  // ...
})
```

Transform (dbt, SQL, etc):
```sql
INSERT INTO landing.transactions (ingestion_id, loaded_at, id, amount, currency, raw_payload)
SELECT
  JSON_EXTRACT_SCALAR(payload, '$.ingestion_id') as ingestion_id,
  JSON_EXTRACT(payload, '$.ingestion_loaded_at') as loaded_at,
  JSON_EXTRACT_SCALAR(payload, '$.id') as id,
  JSON_EXTRACT(payload, '$.amount') as amount,
  JSON_EXTRACT_SCALAR(payload, '$.currency') as currency,
  payload as raw_payload
FROM landing.raw_data;
```

## Idempotency: Your Responsibility

The `ingestion_id` is deterministic UUID v5, derived from:
```
namespace = e3a4f8c0-1b9d-4ea0-9c2e-77f6a6c4a4d7
key = {provider}|{entity}|{source_key}|{record_ts}
id = SHA1(namespace, key)
```

**Use it for deduplication:**
```sql
-- Deduplicate on load
CREATE TEMP TABLE new_data AS
SELECT * FROM staging.raw_data
WHERE JSON_EXTRACT_SCALAR(payload, '$.ingestion_id') NOT IN (
  SELECT ingestion_id FROM landing.transactions
);

INSERT INTO landing.transactions
SELECT ... FROM new_data;
```

Or:
```sql
-- Merge on duplicate
MERGE landing.transactions t
USING staging.raw_data s
ON t.ingestion_id = JSON_EXTRACT_SCALAR(s.payload, '$.ingestion_id')
WHEN NOT MATCHED THEN INSERT (...) VALUES (...);
```

## Comparison: SDK vs Opinionated Alternative

| Aspect | Bravis | Opinionated SDK |
|--------|--------|-----------------|
| Schema | You define | SDK enforces |
| Flexibility | High (any JSON shape) | Low (must fit mold) |
| Learning curve | Slightly higher | Slightly lower |
| Future-proofing | High (schema evolves independently) | Low (tied to SDK versions) |
| Deduplication | Your business logic | Built-in (but opinionated) |
| Performance tuning | Full control | Limited |
| Multi-tenancy | Easy | Difficult |

## Getting Started

1. **Create your BigQuery table** with whatever schema makes sense
2. **Use `Envelope`** to move data through your pipeline
3. **Add metadata only if you need it** (`ExtraMetadata: true`)
4. **Handle deduplication** in your bronze/silver layer

## FAQ

**Q: Should I always add metadata?**
A: Only if you need the ingestion_id for deduplication or audit trail.

**Q: What if I want different schemas for different data sources?**
A: Create multiple tables. Load each source to its table. No problem.

**Q: Can I change my schema without breaking the SDK?**
A: Yes. Metadata is always in `the provenance fields` fields. Your original data is untouched.

**Q: What about schema validation?**
A: That's your responsibility. Use BigQuery schema validation, dbt contracts, or custom checks.

**Q: How do I handle late-arriving data?**
A: Use the `record_ts` (source timestamp) vs `ingestion_loaded_at` (load time) to detect and handle late arrivals.

---

**Core principle:** SDK provides tools for extraction and loading. **You** decide how to structure your data. This makes the SDK flexible, maintainable, and future-proof.