package record

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"strings"
)

// HTML renders a run record as one self-contained page.
//
// It is a *client of the same document* the JSON export produces, not a second
// view with its own idea of what happened: Build runs first and this renders
// what it returns. That is the same rule the design puts on any front end over
// this surface -- render the artifacts, never become a second way of asking
// (docs/design/07-record.md §6).
//
// No external asset, no CDN, no script. A run record is evidence somebody
// attaches to a ticket or opens six months later on a machine with no network,
// and an asset that fails to load takes the evidence with it.
func HTML(w io.Writer, doc *Document) error {
	d := view{Doc: doc}
	if doc.Describe != nil {
		if b, err := json.MarshalIndent(doc.Describe, "", "  "); err == nil {
			d.DescribeJSON = string(b)
		}
	}
	for _, e := range doc.Timeline {
		row := entryView{Entry: e}
		if len(e.Detail) > 0 {
			row.Detail = detailLine(e.Detail)
		}
		row.Engine = e.Actor == ActorEngine
		d.Rows = append(d.Rows, row)
	}
	return page.Execute(w, d)
}

type view struct {
	Doc          *Document
	DescribeJSON string
	Rows         []entryView
}

type entryView struct {
	Entry
	Detail string
	Engine bool
}

// detailLine keeps the detail readable without hiding it: the keys are sorted by
// Go's map printing being avoided deliberately, since an unstable order in a
// document people compare across runs is a diff that means nothing.
func detailLine(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, m[k]))
	}
	return strings.Join(parts, "  ")
}

var page = template.Must(template.New("record").Parse(`<!doctype html>
<meta charset="utf-8">
<title>{{.Doc.Cluster}} — run record</title>
<style>
 :root { color-scheme: light dark; --line:#8884; --dim:#8888; }
 body { font: 14px/1.5 ui-monospace, SFMono-Regular, Menlo, monospace; margin: 2rem auto; max-width: 60rem; padding: 0 1rem; }
 h1 { font-size: 1.3rem; margin: 0 0 .2rem; }
 h2 { font-size: 1rem; margin: 2rem 0 .5rem; border-bottom: 1px solid var(--line); padding-bottom: .3rem; }
 .sub { color: var(--dim); margin: 0 0 1rem; }
 table { border-collapse: collapse; width: 100%; }
 td, th { text-align: left; vertical-align: top; padding: .25rem .6rem .25rem 0; border-bottom: 1px solid var(--line); }
 th { font-weight: 600; color: var(--dim); font-size: .85rem; }
 .engine td { background: #8881; }
 .detail { color: var(--dim); }
 pre { overflow-x: auto; background: #8881; padding: .8rem; border-radius: 4px; }
 .bad { font-weight: 700; }
 .scroll { overflow-x: auto; }
</style>
<h1>{{.Doc.Cluster}}</h1>
<p class=sub>run record opened {{.Doc.Opened}} · schema {{.Doc.Schema}}</p>

{{if not .Doc.Validity.Valid}}
<p class=bad>This run is not a clean measurement:</p>
<ul>{{range .Doc.Validity.Reasons}}<li>{{.}}</li>{{end}}</ul>
{{end}}

<h2>Role changes</h2>
{{if .Doc.RoleChanges}}
<div class=scroll><table>
<tr><th>at<th>node<th>kind<th>result<th>measured<th>predicted<th>from</tr>
{{range .Doc.RoleChanges}}
<tr><td>{{.At}}<td>{{.Node}}<td>{{.Kind}}<td>{{.Result}}
    <td>{{if .Measured}}{{.Measured}}{{else}}—{{end}}
    <td>{{.Predicted}}<td class=detail>{{.Trigger}}</tr>
{{end}}
</table></div>
<p class=sub>Both intervals, always. Measured is this tool's own event to the
engine's own line; predicted is arithmetic from the settings that were in force,
and the two disagreeing is the finding rather than a bug in either.</p>
{{else}}
<p class=sub>No role change in this run.</p>
{{end}}

<h2>Timeline</h2>
<div class=scroll><table>
<tr><th>at<th>actor<th>event<th>detail</tr>
{{range .Rows}}
<tr class="{{if .Engine}}engine{{end}}"><td>{{.T}}<td>{{.Actor}}<td>{{.Event}}<td class=detail>{{.Detail}}</tr>
{{end}}
</table></div>
<p class=sub>Shaded rows are the engine's own lines, harvested from its logs;
the rest are commands this tool ran. One timeline, two actors, so "the tool cut
the route at 07:12:00" and "the engine logged a failover at 07:12:06" are in the
same column of the same table.</p>

{{if .DescribeJSON}}
<h2>The cluster this ran against</h2>
<p class=sub>As it stood when the record opened, not as it stands now.</p>
<pre>{{.DescribeJSON}}</pre>
{{end}}
`))
