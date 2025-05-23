package diff

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/meilisearch/meilisearch-go"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/transform"
)

// Issue is a finding of the schema check.
type Issue struct {
	Level   string `json:"level"` // error, warning, info
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

// FieldReport describes one document field.
type FieldReport struct {
	Name         string   `json:"name"`
	SourceColumn string   `json:"sourceColumn,omitempty"`
	SourceType   string   `json:"sourceType,omitempty"`
	InSource     bool     `json:"inSource"`
	InIndex      bool     `json:"inIndex"`
	Coverage     float64  `json:"coverage"` // share of documents holding the field
	Settings     []string `json:"settings"`
	Issues       []string `json:"issues"`
}

// PrimaryKeyReport checks key consistency.
type PrimaryKeyReport struct {
	Source []string `json:"source"`
	Field  string   `json:"field"`
	Index  string   `json:"index"`
	OK     bool     `json:"ok"`
}

// SchemaReport is the result of a column check.
type SchemaReport struct {
	TaskID     string           `json:"taskId"`
	IndexUID   string           `json:"indexUid"`
	PrimaryKey PrimaryKeyReport `json:"primaryKey"`
	Fields     []FieldReport    `json:"fields"`
	Issues     []Issue          `json:"issues"`
	Documents  int64            `json:"documents"`
	CheckedAt  time.Time        `json:"checkedAt"`
}

// SchemaInput gathers what the check needs.
type SchemaInput struct {
	Task     model.Task
	Schema   *model.Schema
	Tr       *transform.Transformer
	IndexPK  string
	Stats    *meilisearch.StatsIndex // nil when the index does not exist
	Settings *meilisearch.Settings   // nil when the index does not exist
}

var timeTypes = []string{"timestamp", "date", "time", "datetime"}

func isTimeType(t string) bool {
	t = strings.ToLower(t)
	for _, k := range timeTypes {
		if strings.Contains(t, k) {
			return true
		}
	}
	return false
}

func isBinaryType(t string) bool {
	t = strings.ToLower(t)
	return t == "bytea" || strings.Contains(t, "blob") || strings.Contains(t, "binary")
}

// CheckSchema compares source columns, index fields and settings.
func CheckSchema(in SchemaInput) SchemaReport {
	rep := SchemaReport{TaskID: in.Task.ID, IndexUID: in.Task.Index.UID, CheckedAt: time.Now().UTC(), Fields: []FieldReport{}, Issues: []Issue{}}
	pkField := in.Tr.PrimaryKeyField()
	rep.PrimaryKey = PrimaryKeyReport{Source: in.Tr.PrimaryKey(), Field: pkField, Index: in.IndexPK}
	rep.PrimaryKey.OK = in.Stats == nil || in.IndexPK == "" || in.IndexPK == pkField
	if !rep.PrimaryKey.OK {
		rep.Issues = append(rep.Issues, Issue{Level: "error", Field: pkField,
			Message: fmt.Sprintf("index primary key is %q but the task writes %q", in.IndexPK, pkField)})
	}
	if in.Schema != nil && len(in.Schema.PrimaryKey) == 0 && len(in.Task.Mapping.PrimaryKey) == 0 {
		rep.Issues = append(rep.Issues, Issue{Level: "error", Message: "the source has no primary key; set mapping.primaryKey"})
	}

	fields := map[string]*FieldReport{}
	get := func(name string) *FieldReport {
		f, ok := fields[name]
		if !ok {
			f = &FieldReport{Name: name, Settings: []string{}, Issues: []string{}}
			fields[name] = f
		}
		return f
	}

	pk := get(pkField)
	pk.InSource = true
	pk.SourceColumn = strings.Join(in.Tr.PrimaryKey(), "+")
	if in.Schema != nil {
		for _, c := range in.Schema.Columns {
			name, ok := in.Tr.FieldName(c.Name)
			if !ok {
				continue
			}
			f := get(name)
			f.InSource, f.SourceColumn, f.SourceType = true, c.Name, c.Type
		}
	}
	for k := range in.Task.Mapping.Static {
		get(k).InSource = true
	}
	if in.Task.Mapping.Geo != nil {
		g := get("_geo")
		g.InSource, g.SourceColumn, g.SourceType = true, in.Task.Mapping.Geo.Lat+","+in.Task.Mapping.Geo.Lng, "geo"
	}

	var docs int64
	if in.Stats != nil {
		docs = in.Stats.NumberOfDocuments
		rep.Documents = docs
		for name, n := range in.Stats.FieldDistribution {
			f := get(name)
			f.InIndex = true
			if docs > 0 {
				f.Coverage = float64(n) / float64(docs)
			}
		}
	}

	if in.Settings != nil {
		mark := func(kind string, attrs []string) {
			for _, a := range attrs {
				if a == "*" || a == "" {
					continue
				}
				get(a).Settings = append(get(a).Settings, kind)
			}
		}
		mark("searchable", in.Settings.SearchableAttributes)
		mark("displayed", in.Settings.DisplayedAttributes)
		mark("filterable", in.Settings.FilterableAttributes)
		mark("sortable", in.Settings.SortableAttributes)
		if in.Settings.DistinctAttribute != nil {
			mark("distinct", []string{*in.Settings.DistinctAttribute})
		}
		for _, r := range in.Settings.RankingRules {
			if i := strings.LastIndex(r, ":"); i > 0 {
				mark("ranking", []string{r[:i]})
			}
		}
	}

	for _, f := range fields {
		has := func(kind string) bool {
			for _, s := range f.Settings {
				if s == kind {
					return true
				}
			}
			return false
		}
		switch {
		case !f.InSource && f.InIndex && f.Name != pkField:
			f.Issues = append(f.Issues, "field exists in the index but not in the source (dropped or renamed column?); a zero-downtime resync removes it")
		case f.InSource && !f.InIndex && docs > 0 && f.Name != "_geo":
			f.Issues = append(f.Issues, "column is mapped but no indexed document has it yet")
		}
		if len(f.Settings) > 0 && !f.InSource && !f.InIndex {
			f.Issues = append(f.Issues, fmt.Sprintf("settings reference %s but no document can contain it", strings.Join(f.Settings, ", ")))
		}
		if isTimeType(f.SourceType) && in.Task.Mapping.TimeFormat == model.TimeRFC3339 && (has("filterable") || has("sortable") || has("ranking")) {
			f.Issues = append(f.Issues, "date stored as RFC3339 text cannot be range filtered or sorted chronologically; set mapping.timeFormat to unix")
		}
		if isBinaryType(f.SourceType) {
			f.Issues = append(f.Issues, "binary column is indexed as base64 text; consider excluding it")
		}
		if f.Name == "_geo" && !has("filterable") && !has("sortable") {
			f.Issues = append(f.Issues, "add _geo to filterable/sortable attributes to use geo search")
		}
		if f.InIndex && f.Coverage > 0 && f.Coverage < 1 && f.Name == pkField {
			f.Issues = append(f.Issues, "some documents miss the primary key field")
		}
		sort.Strings(f.Settings)
		rep.Fields = append(rep.Fields, *f)
		for _, msg := range f.Issues {
			level := "warning"
			if strings.HasPrefix(msg, "column is mapped") || strings.HasPrefix(msg, "add _geo") || strings.HasPrefix(msg, "binary") {
				level = "info"
			}
			if strings.HasPrefix(msg, "settings reference") {
				level = "error"
			}
			rep.Issues = append(rep.Issues, Issue{Level: level, Field: f.Name, Message: msg})
		}
	}
	sort.Slice(rep.Fields, func(i, j int) bool {
		if rep.Fields[i].Name == pkField {
			return true
		}
		if rep.Fields[j].Name == pkField {
			return false
		}
		return rep.Fields[i].Name < rep.Fields[j].Name
	})
	order := map[string]int{"error": 0, "warning": 1, "info": 2}
	sort.SliceStable(rep.Issues, func(i, j int) bool { return order[rep.Issues[i].Level] < order[rep.Issues[j].Level] })
	if in.Stats == nil {
		rep.Issues = append(rep.Issues, Issue{Level: "info", Message: "the index does not exist yet; it is created when the task starts"})
	}
	return rep
}
