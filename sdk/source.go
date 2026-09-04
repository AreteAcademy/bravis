package sdk

import (
	"fmt"
	"io"

	core "github.com/AreteAcademy/bravis/sdk/internal/core"
)

// Source says where records come from, and what every origin honours.
//
// The origin itself is From -- from.HTTP, from.Postgres, from.Files. What
// lives here instead of in the driver is what is true of all of them: the
// preview, the counters.
//
//	Source: sdk.Source{
//		From:    from.HTTP{URL: "https://api.example.com/v1/events"},
//		Preview: 5,
//	}
type Source struct {
	// From is the origin. Required.
	From Reader

	// Preview prints the first N records once the read finishes, the way a
	// dataframe's head() shows the top of a frame. Zero prints nothing.
	//
	// It answers "what did I actually just pull?" without a debugger and
	// without draining the stream into a variable. The sample is taken as the
	// records stream past, so it costs N records of memory and never changes
	// what the consumer receives.
	Preview int

	// PreviewBytes caps the printed block. Zero uses 4096. Rows are dropped
	// from the bottom until it fits, and the footer says how many.
	PreviewBytes int

	// PreviewWriter is where the table goes. Nil means os.Stderr.
	PreviewWriter io.Writer

	// Stats, when not nil, is filled in as the read proceeds. Read it after
	// the stream is drained: that is when the counters are final.
	Stats *core.Stats
}

func (s Source) validate() error {
	if s.From == nil {
		return fmt.Errorf("Source.From is required: pass an origin, such as " +
			"from.HTTP{URL: \"https://api.example.com/v1/events\"}")
	}
	return nil
}

// options folds the Source into what every driver receives.
func (s Source) options(run RunContext) core.ReadOptions {
	return core.ReadOptions{
		Preview:       s.Preview,
		PreviewBytes:  s.PreviewBytes,
		PreviewWriter: s.PreviewWriter,
		Stats:         s.Stats,
		Run:           run,
	}
}
