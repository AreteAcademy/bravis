// Command 09-transform shows the step between Extract and Load.
//
// The Open-Meteo response is one object holding two parallel arrays plus a
// pile of request metadata:
//
//	{
//	  "latitude": -23.51, "longitude": -46.61,
//	  "generationtime_ms": 0.02,          // changes on every call
//	  "utc_offset_seconds": 0, "timezone": "GMT",
//	  "timezone_abbreviation": "GMT", "elevation": 737,
//	  "hourly_units": {"temperature_2m": "°C"},
//	  "hourly": {
//	    "time": ["2026-09-03T00:00", ...],
//	    "temperature_2m": [14.1, ...]
//	  }
//	}
//
// Expand turns that into one record per hour. Transform then makes each
// record what you actually want to store.
package main

import (
	"fmt"
	"time"

	"github.com/AreteAcademy/bravis/sdk"
)

func main() {
	sdk.Run(sdk.Pipeline{
		Name: "open_meteo/hourly",

		Source: sdk.Source{
			URL: "https://api.open-meteo.com/v1/forecast" +
				"?latitude=-23.55&longitude=-46.63&hourly=temperature_2m",
			Timeout: 15 * time.Second,
			Guard:   sdk.RejectIf("error"),
			// One record per hour, with latitude, longitude and the other
			// top-level scalars copied onto each.
			Expand: sdk.ParallelArrays("hourly", "time", "temperature_2m"),
		},

		// Runs on every record, in order, before anything is written.
		Transform: []sdk.Transformer{
			// generationtime_ms changes on every call, so keeping it makes
			// the same reading write a different payload every run.
			sdk.Without("generationtime_ms", "timezone_abbreviation", "utc_offset_seconds"),

			// Say what the number is, in the name.
			sdk.Rename(map[string]string{
				"time":           "observed_at",
				"temperature_2m": "temperature_c",
			}),

			// Derive what the source gives you in one unit and you want in two.
			sdk.Compute("temperature_f", func(r map[string]any) (any, error) {
				c, ok := r["temperature_c"].(float64)
				if !ok {
					return nil, fmt.Errorf("temperature_c is missing or not a number")
				}
				return c*9/5 + 32, nil
			}),

			// Anything else you need: an ordinary function of yours.
			// Returning sdk.SkipRecord drops the record.
			func(payload any) (any, error) {
				r := payload.(map[string]any)
				c, _ := r["temperature_c"].(float64)
				if c < -90 || c > 60 {
					return nil, sdk.SkipRecord // outside anything Earth records
				}
				r["is_freezing"] = c <= 0
				return r, nil
			},
		},

		Target: sdk.Target{
			Metadata: &sdk.Metadata{
				Provider: "open_meteo",
				Entity:   "hourly_temperature",
				// Key and When read the record after every Transformer has
				// run, so they name observed_at, not the source's "time".
				Key:  sdk.Key("latitude", "longitude", "observed_at"),
				When: sdk.Field("observed_at"),
			},
		},
	})
}
