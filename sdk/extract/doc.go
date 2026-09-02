// Package extract provides HTTP data extraction with retry, timeout, and format handling.
//
// # Basic usage
//
//	lines, err := extract.CSV(ctx, extract.Fonte{
//		URL: "https://example.gov/api/data.csv",
//	})
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	for env, err := range lines {
//		if err != nil {
//			log.Printf("error: %v", err)
//			continue
//		}
//		// process env
//	}
//
// # Strategy
//
// Extract handles:
//   - Retry on 429, 5xx, and network errors (not on 4xx except 429)
//   - Respect for Retry-After header
//   - Per-attempt and total timeouts (separate)
//   - Stream decoding (never materializing entire response)
//   - Multiple formats: JSON, NDJSON, CSV, XML
//   - Pagination via cursor, offset, or Link headers
//   - Rate limiting via optional rate.Limiter
//   - Guard function to validate 200-OK-but-wrong-format
//   - Automatic redaction of sensitive data in logs
package extract