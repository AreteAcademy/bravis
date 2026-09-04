package core

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Response is one successful HTTP response, handed to Source.Records so the
// fetcher can decide what it means.
//
// The body is already read. Bytes returns it without decoding, so a fetcher
// that only needs to look for a marker pays nothing for the look.
type Response struct {
	// Status is the code, always 2xx. A non-2xx never reaches Records: it is
	// a transport failure, retried where retrying makes sense and reported
	// with its body otherwise.
	//
	// Every 2xx does reach it, including 204 and 206, because what those mean
	// is the vendor's convention and only the fetcher knows it.
	Status int

	// Header is the response's, for a vendor that says something there.
	Header http.Header

	// URL the response came from, already redacted of query-string secrets.
	// Useful in a rejection message.
	URL string

	body []byte
}

// NewResponse builds one. Exported for the extract package; a fetcher
// receives a Response, it does not construct one.
func NewResponse(status int, header http.Header, url string, body []byte) Response {
	return Response{Status: status, Header: header, URL: url, body: body}
}

// Bytes is the body, undecoded.
//
// Cheap on purpose: rejecting a response for a marker in the bytes must not
// cost a full parse of a document already known to be junk.
func (r Response) Bytes() []byte { return r.body }

// JSON decodes the body into v, which is a pointer to whatever shape you
// expect.
//
//	var doc struct {
//		Error  bool   `json:"error"`
//		Reason string `json:"reason"`
//	}
//	if err := r.JSON(&doc); err != nil {
//		return nil, err
//	}
func (r Response) JSON(v any) error {
	if err := json.Unmarshal(r.body, v); err != nil {
		// A rejection, not a plain error: the body not matching is the source
		// sending something that is not data, and that is a different call at
		// three in the morning than a bug in the fetcher.
		return Reject("response %d is not the JSON expected: %v (starts with %s)",
			r.Status, err, snippet(r.body))
	}
	return nil
}

// Object decodes the body as a JSON object, which is the common case and what
// the expansion helpers take.
//
//	doc, err := r.Object()
//	if bad, _ := doc["error"].(bool); bad {
//		return nil, sdk.Reject("the vendor refused: %v", doc["reason"])
//	}
//	return sdk.ParallelArrays("hourly", "time")(doc)
//
// A body that is not a JSON object is an error saying so, rather than an
// empty map -- an HTML error page served with 200 is exactly the case the
// check exists for, and it must not read as "no fields set".
func (r Response) Object() (map[string]any, error) {
	var doc map[string]any
	if err := json.Unmarshal(r.body, &doc); err != nil {
		return nil, Reject("response %d is not a JSON object: %v (starts with %s)",
			r.Status, err, snippet(r.body))
	}
	return doc, nil
}

func snippet(b []byte) string {
	const max = 60
	if len(b) > max {
		return fmt.Sprintf("%q...", b[:max])
	}
	return fmt.Sprintf("%q", b)
}

// Reading receives every successful response and returns the records it
// carries -- or refuses it, saying why.
//
// This is where a fetcher's knowledge of its source lives. Validating and
// slicing are the same question ("what does this response mean?"), and it is
// answered once, per response, before anything is decoded:
//
//	Records: func(r sdk.Response) ([]any, error) {
//		if r.Status == http.StatusNoContent {
//			return nil, nil // an empty window is a result, not a failure
//		}
//		doc, err := r.Object()
//		if err != nil {
//			return nil, err
//		}
//		if bad, _ := doc["error"].(bool); bad {
//			return nil, sdk.Reject("open-meteo refused: %v", doc["reason"])
//		}
//		return sdk.ParallelArrays("hourly", "time", "temperature_2m")(doc)
//	}
//
// Per response, not per record, and that is the point. A response that is an
// error carries zero records, so a per-record check is never called on it --
// the failure would arrive as "0 rows", which says nothing about what the
// vendor actually answered.
//
// It is not a field of Source, because Source is configuration -- URL,
// headers, timeouts, retry, pagination -- and this is the one decision in the
// fetcher that is about the data. It lives on Pipeline, next to Transform,
// which is the other thing that runs over what was extracted.
//
// Nil leaves the SDK's default: decode the body and treat each document as one
// record. That path stays streaming, which matters for a large NDJSON or CSV;
// passing a Reading buffers the response, because a function that decides what
// a response means has to see all of it.
type Reading func(Response) ([]any, error)
