package ui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/twoojoo/lumino/internal/db"
)

// exportFormats lists the file formats (id, label, extension); the label
// and extension of "insert" come from the connection's language.
var exportFormats = [][3]string{
	{"csv", "CSV", ".csv"},
	{"tsv", "TSV", ".tsv"},
	{"json", "JSON", ".json"},
	{"insert", "SQL INSERT", ".sql"},
	{"markdown", "Markdown", ".md"},
}

// exportFormatsFor is exportFormats for a language.
func exportFormatsFor(l db.Language) [][3]string {
	out := make([][3]string, len(exportFormats))
	copy(out, exportFormats)
	for i, f := range out {
		if f[0] == "insert" && l != nil {
			out[i][1], out[i][2] = l.InsertFormat()
		}
	}
	return out
}

// rowWriter streams rows in a format.
type rowWriter struct {
	w      *bufio.Writer
	format string
	cols   []db.Column
	lang   db.Language
	ref    db.ObjectRef
	csv    *csv.Writer
	names  []string // JSON keys (duplicate column names made unique)
	n      int
}

// newRowWriter writes rows in format; lang renders "insert" (nil = generic
// SQL).
func newRowWriter(w io.Writer, format string, cols []db.Column, lang db.Language, ref db.ObjectRef, header bool) (*rowWriter, error) {
	if lang == nil {
		lang = db.GenericSQL
	}
	rw := &rowWriter{w: bufio.NewWriterSize(w, 256*1024), format: format, cols: cols, lang: lang, ref: ref}
	switch format {
	case "csv", "tsv", "tsv-noheader":
		rw.csv = csv.NewWriter(rw.w)
		if format != "csv" {
			rw.csv.Comma = '\t'
		}
		if header && format != "tsv-noheader" {
			names := make([]string, len(cols))
			for i, c := range cols {
				names[i] = c.Name
			}
			if err := rw.csv.Write(names); err != nil {
				return nil, err
			}
			rw.csv.Flush() // rows are encoded directly on rw.w
			if err := rw.csv.Error(); err != nil {
				return nil, err
			}
		}
	case "json":
		seen := map[string]int{}
		rw.names = make([]string, len(cols))
		for i, c := range cols {
			name := c.Name
			if n := seen[name]; n > 0 {
				name = fmt.Sprintf("%s_%d", name, n+1)
			}
			seen[c.Name]++
			rw.names[i] = name
		}
		rw.w.WriteString("[\n")
	case "markdown":
		names := make([]string, len(cols))
		seps := make([]string, len(cols))
		for i, c := range cols {
			names[i] = mdEscape(c.Name)
			seps[i] = "---"
			if c.Kind.Numeric() {
				seps[i] = "---:"
			}
		}
		fmt.Fprintf(rw.w, "| %s |\n| %s |\n", strings.Join(names, " | "), strings.Join(seps, " | "))
	case "insert":
	default:
		return nil, fmt.Errorf("unknown format %q", format)
	}
	return rw, nil
}

func mdEscape(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ", "\r", "").Replace(s)
}

// jsonValue converts a row value for encoding/json.
func jsonValue(v any, k db.Kind) any {
	switch x := v.(type) {
	case nil, bool, int64, uint64:
		return x
	case float64:
		return x
	case string:
		if k == db.KindDecimal || k == db.KindInt {
			if _, ok := new(big.Rat).SetString(x); ok {
				return json.Number(x)
			}
		}
		if k == db.KindJSON && json.Valid([]byte(x)) {
			return json.RawMessage(x)
		}
		return x
	case time.Time:
		return db.FormatTime(x, k)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = jsonValue(e, db.KindOther)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for kk, e := range x {
			out[kk] = jsonValue(e, db.KindOther)
		}
		return out
	}
	return db.FormatValue(v, k)
}

func (rw *rowWriter) write(row []any) error {
	switch rw.format {
	case "csv", "tsv", "tsv-noheader":
		// NULL is an empty field, an empty string is "" (csv.Writer can't
		// tell them apart, so fields are encoded here)
		sep := byte(',')
		if rw.format != "csv" {
			sep = '\t'
		}
		for i, v := range row {
			if i > 0 {
				rw.w.WriteByte(sep)
			}
			if v == nil {
				continue
			}
			f := db.FormatValue(v, rw.cols[i].Kind)
			if f == "" || strings.ContainsAny(f, "\"\r\n"+string(sep)) || f[0] == ' ' || f[len(f)-1] == ' ' {
				f = `"` + strings.ReplaceAll(f, `"`, `""`) + `"`
			}
			rw.w.WriteString(f)
		}
		rw.w.WriteString("\n")
	case "json":
		obj := make([]string, len(row))
		for i, v := range row {
			k, _ := json.Marshal(rw.names[i])
			val, err := json.Marshal(jsonValue(v, rw.cols[i].Kind))
			if err != nil {
				val, _ = json.Marshal(db.FormatValue(v, rw.cols[i].Kind))
			}
			obj[i] = string(k) + ": " + string(val)
		}
		if rw.n > 0 {
			rw.w.WriteString(",\n")
		}
		rw.w.WriteString("  {" + strings.Join(obj, ", ") + "}")
	case "markdown":
		cells := make([]string, len(row))
		for i, v := range row {
			if v == nil {
				cells[i] = "NULL"
			} else {
				cells[i] = mdEscape(db.FormatValue(v, rw.cols[i].Kind))
			}
		}
		fmt.Fprintf(rw.w, "| %s |\n", strings.Join(cells, " | "))
	case "insert":
		q := rw.lang.InsertText(rw.ref, rw.cols, row)
		if q == "" {
			return fmt.Errorf("row %d: can't build an insert statement", rw.n+1)
		}
		rw.w.WriteString(q + "\n")
	}
	rw.n++
	return nil
}

func (rw *rowWriter) close() error {
	if rw.csv != nil {
		rw.csv.Flush()
		if err := rw.csv.Error(); err != nil {
			return err
		}
	}
	if rw.format == "json" {
		rw.w.WriteString("\n]\n")
	}
	return rw.w.Flush()
}

// writeRows writes a block of rows (clipboard copies).
func writeRows(w io.Writer, format string, cols []db.Column, rows [][]any, lang db.Language, ref db.ObjectRef, header bool) error {
	rw, err := newRowWriter(w, format, cols, lang, ref, header)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := rw.write(r); err != nil {
			return err
		}
	}
	return rw.close()
}

// maxClipboardExport bounds an export to the clipboard (bigger results go
// to a file).
const maxClipboardExport = 64 << 20

// capBuffer is a bytes.Buffer refusing to grow past max.
type capBuffer struct {
	b   bytes.Buffer
	max int
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if c.b.Len()+len(p) > c.max {
		return 0, fmt.Errorf("more than %s: too large for the clipboard, export to a file", db.HumanBytes(int64(c.max)))
	}
	return c.b.Write(p)
}

// ExportDialog exports a result to a file or the clipboard.
type ExportDialog struct {
	app     *App
	connID  string
	rs      *resultSet
	query   string // statement re-run for "all rows" ("" = not possible)
	ref     db.ObjectRef
	schema  string // session schema of the console ("all rows" re-runs there)
	format  int
	formats [][3]string // exportFormatsFor the connection
	all     bool
	path    widget.Editor
	done    bool
	d       dialog

	running  bool
	written  atomic.Int64
	cancel   context.CancelFunc
	result   string
	fmtBtns  []widget.Clickable
	scopeBtn [2]widget.Clickable
	destBtn  [2]widget.Clickable // file, clipboard
	toClip   bool
	ok, stop widget.Clickable
}

// newExportDialog exports rs; table (optional) names the INSERT target and
// enables re-running the browse query.
func newExportDialog(a *App, connID string, rs *resultSet, table *db.ObjectRef) *ExportDialog {
	cs := a.connState(connID)
	d := &ExportDialog{app: a, connID: connID, rs: rs, fmtBtns: make([]widget.Clickable, len(exportFormats)), formats: exportFormatsFor(cs.language())}
	d.ref = db.ObjectRef{Name: "exported"}
	if table != nil {
		d.ref = *table
	}
	if cs.language().Classify(rs.sql) == db.StmtRead {
		d.query = rs.sql
	}
	// re-running is needed only when the cursor has more rows
	d.all = rs.more && d.query != ""
	d.path.SingleLine = true
	d.setPath()
	return d
}

func (d *ExportDialog) setPath() {
	name := d.ref.Name
	if name == "" || name == "exported" {
		name = "result"
	}
	name += "-" + time.Now().Format("20060102-150405") + d.formats[d.format][2]
	d.path.SetText(filepath.Join(downloadsDir(), name))
}

func (d *ExportDialog) Done() bool { return d.done }

func (d *ExportDialog) cancelled() {
	if d.cancel != nil {
		d.cancel()
	}
}

func (d *ExportDialog) start() {
	a := d.app
	toClip := d.toClip
	path := strings.TrimSpace(d.path.Text())
	if !toClip {
		if path == "" {
			return
		}
		if strings.HasPrefix(path, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(home, path[2:])
			}
		}
		if _, err := os.Stat(path); err == nil {
			d.result = "File exists: choose another name"
			return
		}
	}
	format := d.formats[d.format][0]
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel, d.running, d.result = cancel, true, ""
	d.written.Store(0)
	cols := d.rs.cols
	// fetched rows in the order shown (client sort)
	rows := make([]db.Row, d.rs.Rows())
	for i := range rows {
		rows[i] = d.rs.rows[d.rs.row(i)]
	}
	all, query, schema := d.all, d.query, d.schema
	cs := a.connState(d.connID)
	conn := cs.conn
	ref := d.ref
	ro := a.connReadOnly(d.connID)
	go func() {
		created := false
		var clip *capBuffer
		err := func() error {
			var f io.Writer
			if toClip {
				clip = &capBuffer{max: maxClipboardExport}
				f = clip
			} else {
				file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
				if err != nil {
					return err
				}
				created = true
				defer file.Close()
				f = file
			}
			var dia db.Language
			if conn != nil {
				dia = conn.Language()
			}
			if !all {
				rw, err := newRowWriter(f, format, cols, dia, ref, true)
				if err != nil {
					return err
				}
				for _, r := range rows {
					if err := rw.write(r); err != nil {
						return err
					}
					d.written.Add(1)
				}
				return rw.close()
			}
			if conn == nil {
				return fmt.Errorf("not connected")
			}
			s, err := conn.Session(ctx)
			if err != nil {
				return err
			}
			defer s.Close()
			// the same context as the statement shown: schema and policy
			if ro {
				if err := s.SetReadOnly(ctx, true); err != nil && !errors.Is(err, db.ErrNotSupported) {
					return fmt.Errorf("read-only session: %w", err)
				}
			}
			if schema != "" {
				if err := s.SetCurrentSchema(ctx, schema); err != nil && !errors.Is(err, db.ErrNotSupported) {
					return fmt.Errorf("schema %s: %w", schema, err)
				}
			}
			res, err := s.Execute(ctx, query)
			if err != nil {
				return err
			}
			defer res.Close()
			rw, err := newRowWriter(f, format, res.Columns(), dia, ref, true)
			if err != nil {
				return err
			}
			for {
				batch, more, err := res.Fetch(ctx, 5000)
				if err != nil {
					return err
				}
				for _, r := range batch {
					if err := rw.write(r); err != nil {
						return err
					}
				}
				d.written.Add(int64(len(batch)))
				a.invalidate()
				if !more {
					break
				}
			}
			return rw.close()
		}()
		if err != nil && created {
			os.Remove(path) // only the partial file this export created
		}
		a.post(func() {
			d.running = false
			if err != nil {
				d.result = "Export failed: " + err.Error()
				return
			}
			if toClip {
				a.copyLaterMsg(strings.TrimSuffix(clip.b.String(), "\n"),
					fmt.Sprintf("Copied %d rows to the clipboard (%s)", d.written.Load(), d.formats[d.format][1]))
			} else {
				a.flash(fmt.Sprintf("Exported %d rows to %s", d.written.Load(), path), false)
			}
			d.done = true
		})
	}()
}

func (d *ExportDialog) Layout(gtx C) D {
	th := d.app.th
	for i := range d.fmtBtns {
		if d.fmtBtns[i].Clicked(gtx) && !d.running {
			d.format = i
			d.setPath()
		}
	}
	for i := range d.destBtn {
		if d.destBtn[i].Clicked(gtx) && !d.running {
			d.toClip = i == 1
			d.result = ""
		}
	}
	if d.scopeBtn[0].Clicked(gtx) {
		d.all = false
	}
	if d.scopeBtn[1].Clicked(gtx) && d.query != "" {
		d.all = true
	}
	if d.ok.Clicked(gtx) && !d.running {
		d.start()
	}
	if d.stop.Clicked(gtx) || d.d.close.Clicked(gtx) {
		if d.running && d.cancel != nil {
			d.cancel()
		} else {
			d.done = true
		}
	}
	labels := make([]string, len(d.formats))
	for i, f := range d.formats {
		labels[i] = f[1]
	}
	return d.d.layout(gtx, th, 560, icExport, "Export", func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return sectionTitle(gtx, th, "Format") }),
			layout.Rigid(func(gtx C) D { return segmented(gtx, th, d.fmtBtns, labels, d.format) }),
			layout.Rigid(func(gtx C) D { return sectionTitle(gtx, th, "Rows") }),
			layout.Rigid(func(gtx C) D {
				allLabel := "All rows (runs the query again)"
				if d.query == "" {
					allLabel = "All rows (not available: the statement isn't a query)"
				}
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						return radio(gtx, th, &d.scopeBtn[0], fmt.Sprintf("Fetched rows (%d)", len(d.rs.rows)), !d.all)
					}),
					layout.Rigid(func(gtx C) D { return radio(gtx, th, &d.scopeBtn[1], allLabel, d.all) }),
				)
			}),
			layout.Rigid(func(gtx C) D { return sectionTitle(gtx, th, "Destination") }),
			layout.Rigid(func(gtx C) D {
				sel := 0
				if d.toClip {
					sel = 1
				}
				return segmented(gtx, th, d.destBtn[:], []string{"File", "Clipboard"}, sel)
			}),
			layout.Rigid(func(gtx C) D {
				if d.toClip {
					return layout.Inset{Top: 8}.Layout(gtx, func(gtx C) D {
						return lbl(gtx, th, fontSans, 12, pal.TextDim, "Copied as text in the chosen format (up to "+db.HumanBytes(maxClipboardExport)+")")
					})
				}
				return layout.Inset{Top: 8}.Layout(gtx, func(gtx C) D { return textField(gtx, th, &d.path, "path", true) })
			}),
			layout.Rigid(func(gtx C) D {
				msg, c := d.result, pal.Err
				if d.running {
					msg, c = fmt.Sprintf("Exporting… %d rows written", d.written.Load()), pal.Text
				}
				if msg == "" {
					return D{}
				}
				return layout.Inset{Top: 10}.Layout(gtx, func(gtx C) D { return lbl(gtx, th, fontSans, 12, c, msg) })
			}),
		)
	}, footerButtons(nil,
		func(gtx C) D {
			txt := "Cancel"
			if d.running {
				txt = "Stop"
			}
			return pillButton(gtx, th, &d.stop, nil, txt, pal.Text, pal.Hover)
		},
		func(gtx C) D {
			c := pal.Accent
			if d.running {
				c = pal.Border
			}
			label := "Export"
			if d.toClip {
				label = "Copy"
			}
			return button(gtx, th, &d.ok, label, c)
		},
	))
}

// radio is a radio button with a label.
func radio(gtx C, th *material.Theme, cl *widget.Clickable, label string, on bool) D {
	return cl.Layout(gtx, func(gtx C) D {
		ic, c := icRadioOff, pal.TextDim
		if on {
			ic, c = icRadioOn, pal.Accent
		}
		return insetHV(2, 4).Layout(gtx, func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return mdi(gtx, ic, c, 17) }),
				layout.Rigid(layout.Spacer{Width: 8}.Layout),
				layout.Flexed(1, func(gtx C) D { return lbl(gtx, th, fontSans, 12.5, pal.Text, label) }),
			)
		})
	})
}
