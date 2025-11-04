package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/meilisearch/meilisearch-go"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ja7ad/meilisearch-cdc/docs"
	"github.com/ja7ad/meilisearch-cdc/internal/alerts"
	"github.com/ja7ad/meilisearch-cdc/internal/audit"
	"github.com/ja7ad/meilisearch-cdc/internal/auth"
	"github.com/ja7ad/meilisearch-cdc/internal/diff"
	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

// Out is the structured result of every tool: always a JSON object.
type Out = map[string]any

type toolDef struct {
	info     ToolInfo
	register func(*mcp.Server, *Service)
}

// tool defines a typed tool. The input schema is inferred from In; the
// handler runs as the token's principal after a policy check, and
// mutating calls are audited.
func tool[In any](name, title, desc string, min auth.Role, mutating bool,
	fn func(ctx context.Context, s *Service, in In) (Out, error)) toolDef {
	info := ToolInfo{Name: name, Title: title, Description: desc, MinRole: min, Mutating: mutating}
	return toolDef{info: info, register: func(srv *mcp.Server, s *Service) {
		destructive := mutating
		ann := &mcp.ToolAnnotations{Title: title, ReadOnlyHint: !mutating, DestructiveHint: &destructive}
		mcp.AddTool(srv, &mcp.Tool{Name: name, Title: title, Description: desc, Annotations: ann},
			func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
				p := principal(req.Extra.TokenInfo)
				if p == nil || !s.allowedTool(p.Role, name) {
					return failure(fmt.Errorf("the %s role may not use %s", roleOf(p), name)), nil, nil
				}
				ctx = auth.WithPrincipal(ctx, p)
				out, err := fn(ctx, s, in)
				if mutating && err == nil {
					args, _ := json.Marshal(in)
					s.audit.Record(audit.Entry{UserID: p.ID, UserEmail: p.Label(), Action: "mcp." + name, Details: truncate(string(args), 500)})
				}
				if err != nil {
					return failure(err), nil, nil
				}
				return nil, out, nil
			})
	}}
}

func roleOf(p *auth.Principal) string {
	if p == nil {
		return "anonymous"
	}
	return string(p.Role)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// failure reports a tool error to the model instead of a protocol error.
func failure(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
}

// asTask converts free-form JSON into a task (durations accept "1s").
func asTask(in map[string]any) (model.Task, error) {
	var t model.Task
	raw, err := json.Marshal(in)
	if err != nil {
		return t, err
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return t, fmt.Errorf("invalid task: %w", err)
	}
	return t, nil
}

// object marshals v through JSON into a map for structured output.
func object(key string, v any) Out {
	raw, _ := json.Marshal(v)
	var any any
	_ = json.Unmarshal(raw, &any)
	return Out{key: any}
}

// Inputs.
type (
	idIn struct {
		ID string `json:"id" jsonschema:"the task id"`
	}
	listTasksIn struct {
		State string `json:"state,omitempty" jsonschema:"filter by state, e.g. streaming, failed, held"`
		Query string `json:"query,omitempty" jsonschema:"text matched against name, id, index and table"`
		Tag   string `json:"tag,omitempty"`
	}
	logsIn struct {
		ID    string `json:"id"`
		Limit int    `json:"limit,omitempty" jsonschema:"maximum entries, default 100"`
	}
	taskIn struct {
		Task  map[string]any `json:"task" jsonschema:"the task definition as in the REST API (name, source, index, mapping, sync)"`
		Start bool           `json:"start,omitempty" jsonschema:"start the task right away"`
	}
	updateTaskIn struct {
		ID   string         `json:"id"`
		Task map[string]any `json:"task" jsonschema:"the full task definition; masked secrets keep their stored value"`
	}
	deleteTaskIn struct {
		ID          string `json:"id"`
		DropSource  bool   `json:"dropSource,omitempty" jsonschema:"also drop replication slots and other server side resources"`
		DeleteIndex bool   `json:"deleteIndex,omitempty" jsonschema:"also delete the Meilisearch index"`
	}
	actionIn struct {
		ID     string `json:"id"`
		Action string `json:"action" jsonschema:"one of start, stop, pause, resume, restart, resync, reset-checkpoint"`
	}
	batchIn struct {
		IDs    []string `json:"ids"`
		Action string   `json:"action" jsonschema:"one of start, stop, pause, resume, restart, resync"`
	}
	testIn struct {
		Task map[string]any `json:"task" jsonschema:"a task definition to try without saving it"`
	}
	diffIn struct {
		ID               string `json:"id"`
		CompareFields    bool   `json:"compareFields,omitempty" jsonschema:"compare field values, not only ids"`
		RepairMissing    bool   `json:"repairMissing,omitempty"`
		RepairMismatched bool   `json:"repairMismatched,omitempty"`
		RepairExtra      bool   `json:"repairExtra,omitempty" jsonschema:"delete documents that no longer exist in the source"`
	}
	dlqIn struct {
		ID    string   `json:"id"`
		IDs   []string `json:"ids,omitempty" jsonschema:"dead letter ids; empty means all"`
		Limit int      `json:"limit,omitempty"`
	}
	noIn   struct{}
	destIn struct {
		Destination string `json:"destination,omitempty" jsonschema:"destination id; empty means the default Meilisearch instance"`
	}
	indexIn struct {
		UID         string `json:"uid" jsonschema:"the index uid"`
		Destination string `json:"destination,omitempty" jsonschema:"destination id; empty means the default Meilisearch instance"`
	}
	searchIn struct {
		UID         string   `json:"uid"`
		Destination string   `json:"destination,omitempty"`
		Query       string   `json:"query,omitempty"`
		Filter      string   `json:"filter,omitempty" jsonschema:"Meilisearch filter expression"`
		Sort        []string `json:"sort,omitempty" jsonschema:"e.g. price:asc"`
		Limit       int64    `json:"limit,omitempty"`
	}
	settingsIn struct {
		UID         string         `json:"uid"`
		Destination string         `json:"destination,omitempty"`
		Settings    map[string]any `json:"settings" jsonschema:"Meilisearch index settings, same JSON as PATCH /indexes/{uid}/settings"`
	}
	meiliTasksIn struct {
		Destination string `json:"destination,omitempty"`
		IndexUID    string `json:"indexUid,omitempty"`
		Status      string `json:"status,omitempty" jsonschema:"enqueued, processing, succeeded, failed or canceled"`
		Limit       int64  `json:"limit,omitempty"`
	}
	ruleIn struct {
		Rule map[string]any `json:"rule" jsonschema:"an alert rule as in the REST API"`
	}
	eventsIn struct {
		RuleID string `json:"ruleId,omitempty"`
		State  string `json:"state,omitempty" jsonschema:"firing or resolved"`
		Limit  int    `json:"limit,omitempty"`
	}
)

func limit(n, def int) int {
	if n <= 0 {
		return def
	}
	return min(n, 1000)
}

// catalog is every tool, in display order. It is built in init because
// the tool handlers reach back to the default policy, which reads it.
var (
	catalog       []toolDef
	toolByName    = map[string]toolDef{}
	resources     []resourceDef
	resourceByURI = map[string]resourceDef{}
)

func init() {
	catalog = buildCatalog()
	for _, t := range catalog {
		toolByName[t.info.Name] = t
	}
	resources = buildResources()
	for _, r := range resources {
		resourceByURI[r.info.URI] = r
	}
}

func buildCatalog() []toolDef {
	return []toolDef{
		tool("overview", "Overview", "Summary of all tasks, throughput and the Meilisearch instance.", auth.Viewer, false,
			func(ctx context.Context, s *Service, _ noIn) (Out, error) {
				return object("overview", s.eng.Overview(ctx, source.Registered())), nil
			}),
		tool("list_tasks", "List tasks", "List CDC tasks with their live status (secrets are redacted).", auth.Viewer, false,
			func(_ context.Context, s *Service, in listTasksIn) (Out, error) {
				v, err := s.eng.List(in.State, in.Query, in.Tag)
				return object("tasks", v), err
			}),
		tool("get_task", "Get task", "A task definition and its live status, counters, lag and checkpoint.", auth.Viewer, false,
			func(_ context.Context, s *Service, in idIn) (Out, error) {
				v, err := s.eng.View(in.ID)
				return object("task", v), err
			}),
		tool("get_task_logs", "Task logs", "Recent activity log of a task, newest last.", auth.Viewer, false,
			func(_ context.Context, s *Service, in logsIn) (Out, error) {
				if _, err := s.eng.Get(in.ID); err != nil {
					return nil, err
				}
				return object("logs", s.eng.Logs(in.ID, limit(in.Limit, 100))), nil
			}),
		tool("schema_check", "Schema check", "Compare source columns, index fields and index settings; lists issues.", auth.Viewer, false,
			func(ctx context.Context, s *Service, in idIn) (Out, error) {
				r, err := s.eng.SchemaCheck(ctx, in.ID)
				return object("report", r), err
			}),
		tool("get_diff", "Get diff report", "The last document diff between source and index.", auth.Viewer, false,
			func(_ context.Context, s *Service, in idIn) (Out, error) {
				r, ok, err := s.eng.DiffReport(in.ID)
				if err == nil && !ok {
					return Out{"status": "none"}, nil
				}
				return object("report", r), err
			}),
		tool("list_dlq", "List dead letters", "Documents Meilisearch rejected for a task.", auth.Viewer, false,
			func(_ context.Context, s *Service, in dlqIn) (Out, error) {
				v, err := s.eng.DLQ(in.ID, limit(in.Limit, 100))
				return object("entries", v), err
			}),
		tool("list_destinations", "List destinations", "Meilisearch instances with health, last sync and the tasks writing to them.", auth.Viewer, false,
			func(ctx context.Context, s *Service, _ noIn) (Out, error) {
				v, err := s.eng.DestinationViews(ctx)
				return object("destinations", v), err
			}),
		tool("list_indexes", "List indexes", "Meilisearch indexes with document counts and the tasks writing them.", auth.Viewer, false,
			func(ctx context.Context, s *Service, in destIn) (Out, error) {
				tg, err := s.eng.Target(in.Destination)
				if err != nil {
					return nil, err
				}
				res, err := tg.Client.ListIndexes(ctx, &meilisearch.IndexesQuery{Limit: 1000})
				if err != nil {
					return nil, err
				}
				stats, _ := tg.Client.GetStats(ctx, nil)
				var out []map[string]any
				for _, ix := range res.Results {
					m := map[string]any{"uid": ix.UID, "primaryKey": ix.PrimaryKey, "destinationId": tg.ID, "managedBy": s.eng.ManagedBy(tg.ID, ix.UID)}
					if stats != nil {
						m["numberOfDocuments"] = stats.Indexes[ix.UID].NumberOfDocuments
					}
					out = append(out, m)
				}
				return object("indexes", out), nil
			}),
		tool("get_index", "Get index", "Index primary key, stats and field distribution.", auth.Viewer, false,
			func(ctx context.Context, s *Service, in indexIn) (Out, error) {
				tg, err := s.eng.Target(in.Destination)
				if err != nil {
					return nil, err
				}
				ix, err := tg.Client.GetIndex(ctx, in.UID)
				if err != nil {
					return nil, err
				}
				st, err := tg.Client.Index(in.UID).GetStats(ctx, nil)
				return object("index", map[string]any{"uid": ix.UID, "destinationId": tg.ID, "primaryKey": ix.PrimaryKey, "stats": st, "managedBy": s.eng.ManagedBy(tg.ID, in.UID)}), err
			}),
		tool("search_index", "Search index", "Run a Meilisearch search to verify synced data.", auth.Viewer, false,
			func(ctx context.Context, s *Service, in searchIn) (Out, error) {
				req := &meilisearch.SearchRequest{Sort: in.Sort, Limit: in.Limit}
				if in.Filter != "" {
					req.Filter = in.Filter
				}
				if req.Limit <= 0 {
					req.Limit = 10
				}
				tg, err := s.eng.Target(in.Destination)
				if err != nil {
					return nil, err
				}
				raw, err := tg.Client.Index(in.UID).SearchRaw(ctx, in.Query, req)
				if err != nil {
					return nil, err
				}
				var v any
				_ = json.Unmarshal(*raw, &v)
				return Out{"result": v}, nil
			}),
		tool("get_index_settings", "Get index settings", "Current Meilisearch settings of an index.", auth.Viewer, false,
			func(ctx context.Context, s *Service, in indexIn) (Out, error) {
				tg, err := s.eng.Target(in.Destination)
				if err != nil {
					return nil, err
				}
				raw, err := tg.Indexes.LiveSettings(ctx, in.UID)
				if err != nil {
					return nil, err
				}
				var v any
				_ = json.Unmarshal(raw, &v)
				return Out{"settings": v}, nil
			}),
		tool("preview_index_settings", "Preview settings", "Show which settings would change, without applying them.", auth.Viewer, false,
			func(ctx context.Context, s *Service, in settingsIn) (Out, error) {
				tg, err := s.eng.Target(in.Destination)
				if err != nil {
					return nil, err
				}
				raw, _ := json.Marshal(in.Settings)
				v, err := tg.Indexes.Diff(ctx, in.UID, raw)
				return object("changes", v), err
			}),
		tool("list_meili_tasks", "Meilisearch tasks", "Recent Meilisearch tasks (customMetadata cdc:<task> marks CDC writes).", auth.Viewer, false,
			func(ctx context.Context, s *Service, in meiliTasksIn) (Out, error) {
				q := &meilisearch.TasksQuery{Limit: in.Limit}
				if q.Limit <= 0 {
					q.Limit = 20
				}
				if in.IndexUID != "" {
					q.IndexUIDS = []string{in.IndexUID}
				}
				if in.Status != "" {
					q.Statuses = []meilisearch.TaskStatus{meilisearch.TaskStatus(in.Status)}
				}
				tg, err := s.eng.Target(in.Destination)
				if err != nil {
					return nil, err
				}
				v, err := tg.Client.GetTasks(ctx, q)
				return object("tasks", v), err
			}),
		tool("list_alert_rules", "List alert rules", "Alert rules and their channels.", auth.Viewer, false,
			func(_ context.Context, s *Service, _ noIn) (Out, error) {
				v, err := s.alerts.Rules()
				for i := range v {
					v[i] = v[i].Redacted()
				}
				return object("rules", v), err
			}),
		tool("list_alert_events", "Alert history", "Recent alert events, newest first.", auth.Viewer, false,
			func(_ context.Context, s *Service, in eventsIn) (Out, error) {
				v, err := s.alerts.Events(limit(in.Limit, 50), in.RuleID, alerts.EventState(in.State))
				return object("events", v), err
			}),

		tool("task_action", "Task action", "Start, stop, pause, resume, restart, resync or reset the checkpoint of a task.", auth.Moderator, true,
			func(ctx context.Context, s *Service, in actionIn) (Out, error) {
				switch in.Action {
				case "start", "stop", "pause", "resume", "restart", "resync", "reset-checkpoint":
				default:
					return nil, fmt.Errorf("unknown action %q", in.Action)
				}
				v, err := s.eng.Action(ctx, in.ID, in.Action)
				return object("task", v), err
			}),
		tool("batch_task_action", "Batch task action", "Apply a lifecycle action to several tasks.", auth.Moderator, true,
			func(ctx context.Context, s *Service, in batchIn) (Out, error) {
				switch in.Action {
				case "start", "stop", "pause", "resume", "restart", "resync":
				default:
					return nil, fmt.Errorf("unknown action %q", in.Action)
				}
				return object("results", s.eng.Batch(ctx, in.IDs, in.Action)), nil
			}),
		tool("test_connection", "Test connection", "Connect with a task definition, read its schema and sample documents.", auth.Moderator, false,
			func(ctx context.Context, s *Service, in testIn) (Out, error) {
				t, err := asTask(in.Task)
				if err != nil {
					return nil, err
				}
				return object("result", s.eng.TestConnection(ctx, t)), nil
			}),
		tool("run_diff", "Run diff", "Start a document diff (and optional repair) between source and index; poll get_diff.", auth.Moderator, true,
			func(_ context.Context, s *Service, in diffIn) (Out, error) {
				o := diff.Options{CompareFields: in.CompareFields || in.RepairMismatched, RepairMissing: in.RepairMissing,
					RepairMismatched: in.RepairMismatched, RepairExtra: in.RepairExtra}
				return Out{"status": "running"}, s.eng.StartDiff(in.ID, o)
			}),
		tool("replay_dlq", "Replay dead letters", "Resend rejected documents to Meilisearch.", auth.Moderator, true,
			func(ctx context.Context, s *Service, in dlqIn) (Out, error) {
				ok, failed, err := s.eng.ReplayDLQ(ctx, in.ID, in.IDs)
				return Out{"replayed": ok, "failed": failed}, err
			}),

		tool("create_task", "Create task", "Create a CDC task from a definition (see cdc://docs/api).", auth.Admin, true,
			func(_ context.Context, s *Service, in taskIn) (Out, error) {
				t, err := asTask(in.Task)
				if err != nil {
					return nil, err
				}
				v, err := s.eng.Create(t, in.Start)
				return object("task", v), err
			}),
		tool("update_task", "Update task", "Replace a task definition; running tasks restart.", auth.Admin, true,
			func(ctx context.Context, s *Service, in updateTaskIn) (Out, error) {
				t, err := asTask(in.Task)
				if err != nil {
					return nil, err
				}
				v, err := s.eng.Update(ctx, in.ID, t)
				return object("task", v), err
			}),
		tool("delete_task", "Delete task", "Delete a task, optionally with its source resources and index.", auth.Admin, true,
			func(ctx context.Context, s *Service, in deleteTaskIn) (Out, error) {
				idx := ""
				if in.DeleteIndex {
					t, err := s.eng.Get(in.ID)
					if err != nil {
						return nil, err
					}
					idx = t.Index.UID
				}
				return Out{"deleted": in.ID}, s.eng.Delete(ctx, in.ID, in.DropSource, idx)
			}),
		tool("update_index_settings", "Update index settings", "Apply Meilisearch settings to an index and wait for the task.", auth.Admin, true,
			func(ctx context.Context, s *Service, in settingsIn) (Out, error) {
				tg, err := s.eng.Target(in.Destination)
				if err != nil {
					return nil, err
				}
				raw, _ := json.Marshal(in.Settings)
				ctx, cancel := context.WithTimeout(ctx, time.Minute)
				defer cancel()
				t, err := tg.Indexes.ApplySettings(ctx, in.UID, raw)
				if t != nil {
					return Out{"taskUid": t.UID, "status": t.Status}, err
				}
				return nil, err
			}),
		tool("create_alert_rule", "Create alert rule", "Create an alert rule with email, Telegram or webhook channels.", auth.Admin, true,
			func(_ context.Context, s *Service, in ruleIn) (Out, error) {
				var r alerts.Rule
				raw, _ := json.Marshal(in.Rule)
				if err := json.Unmarshal(raw, &r); err != nil {
					return nil, err
				}
				v, err := s.alerts.Create(r)
				return object("rule", v.Redacted()), err
			}),
		tool("delete_alert_rule", "Delete alert rule", "Delete an alert rule.", auth.Admin, true,
			func(_ context.Context, s *Service, in idIn) (Out, error) {
				return Out{"deleted": in.ID}, s.alerts.Delete(in.ID)
			}),
	}
}

type resourceDef struct {
	info     ResourceInfo
	register func(*mcp.Server, *Service)
}

// resource defines a JSON resource; templates contain {id}.
func resource(uri, name, desc string, read func(ctx context.Context, s *Service, id string) (any, error)) resourceDef {
	info := ResourceInfo{URI: uri, Name: name, Description: desc}
	return resourceDef{info: info, register: func(srv *mcp.Server, s *Service) {
		h := func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			p := principal(req.Extra.TokenInfo)
			if p == nil || !s.allowedResource(p.Role, uri) {
				return nil, fmt.Errorf("the %s role may not read %s", roleOf(p), uri)
			}
			got := req.Params.URI
			id := ""
			if strings.Contains(uri, "{id}") {
				prefix, suffix, _ := strings.Cut(uri, "{id}")
				if !strings.HasPrefix(got, prefix) || !strings.HasSuffix(got, suffix) {
					return nil, mcp.ResourceNotFoundError(got)
				}
				id = strings.TrimSuffix(strings.TrimPrefix(got, prefix), suffix)
			}
			v, err := read(auth.WithPrincipal(ctx, p), s, id)
			if err != nil {
				return nil, err
			}
			if text, ok := v.(string); ok {
				return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: got, MIMEType: "text/markdown", Text: text}}}, nil
			}
			raw, err := json.MarshalIndent(v, "", "  ")
			if err != nil {
				return nil, err
			}
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: got, MIMEType: "application/json", Text: string(raw)}}}, nil
		}
		if strings.Contains(uri, "{id}") {
			srv.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: uri, Name: name, Description: desc, MIMEType: "application/json"}, h)
		} else {
			srv.AddResource(&mcp.Resource{URI: uri, Name: name, Description: desc, MIMEType: "application/json"}, h)
		}
	}}
}

func buildResources() []resourceDef {
	return []resourceDef{
		resource("cdc://overview", "overview", "Tasks by state, throughput and Meilisearch health.", func(ctx context.Context, s *Service, _ string) (any, error) {
			return s.eng.Overview(ctx, source.Registered()), nil
		}),
		resource("cdc://tasks", "tasks", "Every task with its status.", func(_ context.Context, s *Service, _ string) (any, error) {
			return s.eng.List("", "", "")
		}),
		resource("cdc://tasks/{id}", "task", "One task with its status.", func(_ context.Context, s *Service, id string) (any, error) {
			return s.eng.View(id)
		}),
		resource("cdc://tasks/{id}/logs", "task logs", "Recent activity of a task.", func(_ context.Context, s *Service, id string) (any, error) {
			if _, err := s.eng.Get(id); err != nil {
				return nil, err
			}
			return s.eng.Logs(id, 200), nil
		}),
		resource("cdc://indexes", "indexes", "Indexes of the default Meilisearch destination.", func(ctx context.Context, s *Service, _ string) (any, error) {
			tg, err := s.eng.Target("")
			if err != nil {
				return nil, err
			}
			return tg.Client.ListIndexes(ctx, &meilisearch.IndexesQuery{Limit: 1000})
		}),
		resource("cdc://destinations", "destinations", "Meilisearch destinations with health and synced tasks.", func(ctx context.Context, s *Service, _ string) (any, error) {
			return s.eng.DestinationViews(ctx)
		}),
		resource("cdc://alerts/active", "active alerts", "Alerts currently firing.", func(_ context.Context, s *Service, _ string) (any, error) {
			return s.alerts.Active()
		}),
		resource("cdc://docs/api", "API reference", "The REST API and task/rule JSON formats.", func(context.Context, *Service, string) (any, error) {
			return docs.API, nil
		}),
	}
}
