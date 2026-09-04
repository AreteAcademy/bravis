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
		return fmt.Errorf("the response is not the JSON expected: %w", err)
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
		return nil, fmt.Errorf("the response is not a JSON object: %w (starts with %s)",
			err, snippet(r.body))
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
