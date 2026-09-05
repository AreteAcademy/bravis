package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The preview exists to answer "what did I actually just pull?" without a
// debugger and without draining the stream into a variable.
//
// It lives here, not in a driver, because ReadOptions offers it to every
// source -- and an option a driver ignores is the defect this SDK keeps
// finding in itself. It is the shape
// of the data, printed the way a dataframe prints its head.
//
// It is rendered to a writer rather than through slog on purpose: slog's
// TextHandler escapes newlines, so a table logged as an attribute arrives as
// one unreadable line of \n. The counters go through slog, where a structured
// number belongs; the table goes where a human can read it.

const (
	// defaultPreviewBytes caps the rendered block. Big enough for a wide
	// reading, small enough that nobody pipes a warehouse through a log.
	defaultPreviewBytes = 4096

	// maxLineWidth elides columns past this point. A row wider than a
	// terminal is not a preview, it is a wall.
	maxLineWidth = 160

	// maxCellWidth truncates one value. A blob of base64 in one field must
	// not push every other column off the screen.
	maxCellWidth = 40
)

// PreviewStats is what the footer reports: the totals the sample came from.
type PreviewStats struct {
	Rows     int
	Pages    int
	Bytes    int64
	Duration time.Duration
}

// RenderPreview lays the sampled records out as a table with a footer, in the
// spirit of a dataframe's head().
//
// Pure: no clock, no network, no logger. The string it returns is the whole
// of what gets printed, which is what makes it assertable in a test.
func RenderPreview(sample []any, budget int, st PreviewStats) string {
	if len(sample) == 0 {
		return fmt.Sprintf("[no rows · %d %s · %s in %s]\n",
			st.Pages, plural(st.Pages, "page", "pages"),
			humanBytes(st.Bytes), RoundDuration(st.Duration))
	}
	if budget <= 0 {
		budget = defaultPreviewBytes
	}

	cols := columnsOf(sample)
	cells := make([][]string, len(sample))
	for i, row := range sample {
		cells[i] = make([]string, len(cols))
		for j, c := range cols {
			cells[i][j] = formatCell(valueAt(row, c, len(cols) == 1))
		}
	}

	widths := make([]int, len(cols))
	numeric := make([]bool, len(cols))
	for j, c := range cols {
		widths[j] = len([]rune(c))
		numeric[j] = true
		for i := range cells {
			if w := len([]rune(cells[i][j])); w > widths[j] {
				widths[j] = w
			}
			if !looksNumeric(cells[i][j]) {
				numeric[j] = false
			}
		}
	}

	// The index column, the way a dataframe prints one.
	index := len(strconv.Itoa(len(sample) - 1))

	// Elide columns that push the line past a terminal's width.
	shown, width := 0, index
	for j := range cols {
		next := width + 2 + widths[j]
		if shown > 0 && next > maxLineWidth {
			break
		}
		width = next
		shown++
	}
	hiddenCols := len(cols) - shown

	var b bytes.Buffer
	writeRow := func(label string, values []string) {
		line := strings.Repeat(" ", index-len(label)) + label
		for j := 0; j < shown; j++ {
			line += "  " + align(values[j], widths[j], numeric[j])
		}
		// Padding is what lines the columns up; past the last one it is just
		// invisible noise that shows up in a diff.
		b.WriteString(strings.TrimRight(line, " ") + "\n")
	}

	// The header carries no index, the way a dataframe leaves that corner blank.
	writeRow("", cols[:shown])

	// Rows are dropped from the bottom until the block fits the budget. The
	// footer always says how many were held back -- a preview that quietly
	// showed less than it sampled would be lying about the sample.
	rowsShown := 0
	for i := range cells {
		mark := b.Len()
		writeRow(strconv.Itoa(i), cells[i][:shown])
		if b.Len() > budget && rowsShown > 0 {
			b.Truncate(mark)
			break
		}
		rowsShown++
	}

	return b.String() + "\n" + footer(rowsShown, len(cols), hiddenCols, st) + "\n"
}

func footer(shown, cols, hiddenCols int, st PreviewStats) string {
	var parts []string

	if shown < st.Rows {
		parts = append(parts, fmt.Sprintf("%d of %d rows", shown, st.Rows))
	} else {
		parts = append(parts, fmt.Sprintf("%d %s", st.Rows, plural(st.Rows, "row", "rows")))
	}

	c := fmt.Sprintf("%d %s", cols, plural(cols, "column", "columns"))
	if hiddenCols > 0 {
		c += fmt.Sprintf(" (%d not shown)", hiddenCols)
	}
	parts = append(parts, c)
	parts = append(parts, humanBytes(st.Bytes))
	parts = append(parts, fmt.Sprintf("%d %s in %s",
		st.Pages, plural(st.Pages, "page", "pages"), RoundDuration(st.Duration)))

	if st.Pages > 1 {
		parts = append(parts, fmt.Sprintf("%s/page", RoundDuration(st.Duration/time.Duration(st.Pages))))
	}

	return "[" + strings.Join(parts, " · ") + "]"
}

// columnsOf is the union of the keys across the sample, sorted.
//
// Sorted rather than first-seen because a Go map has no order to preserve:
// first-seen would put the columns somewhere different on every run, and a
// preview that reshuffles itself is one you cannot compare against the last.
func columnsOf(sample []any) []string {
	seen := map[string]bool{}
	scalar := false

	for _, row := range sample {
		m, ok := row.(map[string]any)
		if !ok {
			scalar = true
			continue
		}
		for k := range m {
			seen[k] = true
		}
	}

	if len(seen) == 0 && scalar {
		return []string{"value"}
	}

	cols := make([]string, 0, len(seen))
	for k := range seen {
		cols = append(cols, k)
	}
	sort.Strings(cols)
	return cols
}

// valueAt reads one column out of a record, whatever shape the record has.
func valueAt(row any, col string, single bool) any {
	if m, ok := row.(map[string]any); ok {
		return m[col]
	}
	if single {
		return row
	}
	return nil
}

func formatCell(v any) string {
	var s string
	switch t := v.(type) {
	case nil:
		s = "null"
	case string:
		s = t
	case bool:
		s = strconv.FormatBool(t)
	case float64:
		s = strconv.FormatFloat(t, 'g', -1, 64)
	case json.Number:
		s = t.String()
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		s = fmt.Sprint(t)
	default:
		if raw, err := json.Marshal(t); err == nil {
			s = string(raw)
		} else {
			s = fmt.Sprint(t)
		}
	}

	s = strings.ReplaceAll(strings.ReplaceAll(s, "\n", "\\n"), "\t", "\\t")

	if r := []rune(s); len(r) > maxCellWidth {
		s = string(r[:maxCellWidth-1]) + "…"
	}
	return s
}

// align right-justifies a column whose every value reads as a number, and
// left-justifies the rest -- so figures line up on the decimal and prose does
// not float away from its header.
func align(s string, width int, numeric bool) string {
	pad := width - len([]rune(s))
	if pad < 0 {
		pad = 0
	}
	if numeric {
		return strings.Repeat(" ", pad) + s
	}
	return s + strings.Repeat(" ", pad)
}

func looksNumeric(s string) bool {
	if s == "" || s == "null" {
		return true // a gap does not make the column prose
	}
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// humanBytes renders a size the way a person reads one. Decimal units,
// because that is what every storage console and cloud bill uses.
func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 4 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTP"[exp])
}

// RoundDuration keeps a duration readable at whatever scale it lands on.
// Rounding everything to milliseconds turns a fast extract into "0s", which
// reads as "not measured" rather than "quick".
func RoundDuration(d time.Duration) time.Duration {
	switch {
	case d >= time.Second:
		return d.Round(10 * time.Millisecond)
	case d >= time.Millisecond:
		return d.Round(time.Millisecond)
	case d >= time.Microsecond:
		return d.Round(time.Microsecond)
	default:
		return d
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// WritePreview escreve o preview onde o chamador pediu, ou em stderr.
//
// Existe para que cada driver novo nao repita a escolha do destino e o
// tratamento do writer nil -- foi assim que o preview nasceu dentro do extract
// e teve de ser movido quando o segundo driver apareceu.
func WritePreview(w io.Writer, amostra []any, orcamento int, st PreviewStats) {
	if w == nil {
		w = os.Stderr
	}
	_, _ = io.WriteString(w, RenderPreview(amostra, orcamento, st))
}

// LogExtract emite a linha de resumo que todo driver de leitura deve emitir,
// com as mesmas chaves -- para que "quantas linhas e quanto tempo" se leia
// igual, venha de HTTP, de arquivo ou de banco.
func LogExtract(ctx context.Context, driver, fonte string, st PreviewStats) {
	args := []any{
		"driver", driver,
		"source", fonte,
		"rows", st.Rows,
		"duration", RoundDuration(st.Duration),
	}
	if st.Pages > 1 {
		args = append(args, "pages", st.Pages)
	}
	if st.Bytes > 0 {
		args = append(args, "bytes", st.Bytes)
	}
	slog.InfoContext(ctx, "extract complete", args...)
}
