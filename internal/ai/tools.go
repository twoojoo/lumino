package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/duration"
	"sigs.k8s.io/yaml"

	"github.com/giovanni/lumino/internal/backend"
)

const maxToolOutput = 14000

// Proposal is a state change suggested by the model; it is never executed
// by the agent, only shown to the user for confirmation.
type Proposal struct {
	Action    string `json:"action"` // scale | restart | delete | apply | cordon | uncordon
	Kind      string `json:"kind"`
	GVR       string `json:"-"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Replicas  *int32 `json:"replicas,omitempty"`
	Manifest  string `json:"manifest,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// Path is the k9s path (ns/name or name).
func (p *Proposal) Path() string {
	if p.Namespace != "" && backend.IsNamespaced(p.GVR) {
		return p.Namespace + "/" + p.Name
	}
	return p.Name
}

// Tools executes the assistant's tools against a cluster.
type Tools struct {
	Cluster   *backend.Cluster
	Namespace string // UI namespace, used as default
	OnPropose func(*Proposal)
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

// Specs lists the tools exposed to the model.
func (t *Tools) Specs() []ToolSpec {
	kindDesc := "Resource kind: plural, singular, short name or Kind (pods, deploy, svc, Deployment, a CRD name…)"
	nsDesc := "Namespace; omit to use the namespace currently selected in the UI, use \"all\" for every namespace"
	mk := func(name, desc string, props map[string]any, req ...string) ToolSpec {
		return ToolSpec{Name: name, Description: desc, Properties: props, Required: req}
	}
	return []ToolSpec{
		mk("cluster_overview", "Summary of cluster health: nodes, pod phases, deployments, CPU/memory requests vs capacity, recent warning events.",
			map[string]any{"namespace": strProp(nsDesc)}),
		mk("list_resources", "List resources of a kind as a table (same columns as k9s). Use filter to narrow by name/status.",
			map[string]any{"kind": strProp(kindDesc), "namespace": strProp(nsDesc), "filter": strProp("Optional case-insensitive regex matched against each row")},
			"kind"),
		mk("get_resource_yaml", "Get the YAML manifest of one resource (secret values are always redacted).",
			map[string]any{"kind": strProp(kindDesc), "name": strProp("Resource name"), "namespace": strProp(nsDesc)},
			"kind", "name"),
		mk("describe_resource", "kubectl-describe style details of one resource, including recent events.",
			map[string]any{"kind": strProp(kindDesc), "name": strProp("Resource name"), "namespace": strProp(nsDesc)},
			"kind", "name"),
		mk("get_events", "List events, newest first.",
			map[string]any{"namespace": strProp(nsDesc), "object": strProp("Only events whose involved object name contains this"), "warnings_only": boolProp("Only Warning events")}),
		mk("get_logs", "Fetch recent log lines of a pod, or of all pods of a deployment/statefulset/daemonset/job/service.",
			map[string]any{
				"kind":       strProp("pods (default), deployments, statefulsets, daemonsets, jobs, services"),
				"name":       strProp("Resource name"),
				"namespace":  strProp(nsDesc),
				"container":  strProp("Container name (default: all containers)"),
				"previous":   boolProp("Logs of the previous (crashed) container instance"),
				"tail_lines": intProp("Lines per container (default 200, max 1000)"),
				"grep":       strProp("Optional case-insensitive regex; only matching lines are returned"),
			}, "name"),
		mk("xray", "Dependency tree of a workload (deployment → replicasets → pods → containers, service accounts, config maps, secrets, PVCs) with health status.",
			map[string]any{"kind": strProp("deployments, statefulsets, daemonsets, replicasets, services or pods"), "name": strProp("Optional resource name to focus on"), "namespace": strProp(nsDesc)},
			"kind"),
		mk("lint_cluster", "Run the Kubeon sanitizer (Popeye-like checks): failing/pending pods, missing requests/limits/probes, floating image tags, privileged containers, unavailable deployments, services without endpoints, broken references, unused config, unbound PVCs, node problems.",
			map[string]any{"namespace": strProp(nsDesc), "min_severity": map[string]any{"type": "string", "enum": []string{"error", "warning", "info"}, "description": "Lowest severity to include (default warning)"}}),
		mk("list_api_resources", "List resource kinds available in the cluster (including CRDs).", map[string]any{}),
		mk("propose_action", "Propose a change for the user to review and confirm. NEVER assume it was executed: the user decides. Use for scale, restart, delete, apply (full manifest), cordon, uncordon.",
			map[string]any{
				"action":    map[string]any{"type": "string", "enum": []string{"scale", "restart", "delete", "apply", "cordon", "uncordon"}},
				"kind":      strProp(kindDesc),
				"name":      strProp("Resource name"),
				"namespace": strProp("Namespace of the resource"),
				"replicas":  intProp("Desired replicas (scale only)"),
				"manifest":  strProp("Complete YAML manifest to apply (apply only)"),
				"reason":    strProp("Short justification shown to the user"),
			}, "action", "kind", "name", "reason"),
	}
}

type toolArgs struct {
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	Filter       string `json:"filter"`
	Object       string `json:"object"`
	WarningsOnly bool   `json:"warnings_only"`
	Container    string `json:"container"`
	Previous     bool   `json:"previous"`
	TailLines    int64  `json:"tail_lines"`
	Grep         string `json:"grep"`
}

func (t *Tools) ns(a string) string {
	switch strings.ToLower(strings.TrimSpace(a)) {
	case "":
		return t.Namespace
	case "all", "*", "-a", "all-namespaces":
		return ""
	}
	return a
}

func resolveKind(k string) (string, error) {
	k = strings.TrimSpace(k)
	if k == "" {
		k = "pods"
	}
	if g, ok := backend.LookupAlias(k); ok {
		return g, nil
	}
	if g, ok := backend.LookupAlias(strings.TrimSuffix(strings.ToLower(k), "s")); ok {
		return g, nil
	}
	return "", fmt.Errorf("unknown kind %q (use list_api_resources)", k)
}

func pathFor(gvr, ns, name string) string {
	if backend.IsNamespaced(gvr) && ns != "" && !strings.Contains(name, "/") {
		return ns + "/" + name
	}
	return name
}

func clip(s string) string {
	if len(s) <= maxToolOutput {
		return s
	}
	return s[:maxToolOutput] + fmt.Sprintf("\n…[truncated %d bytes]", len(s)-maxToolOutput)
}

// Run executes a tool call; errors are returned as error results.
func (t *Tools) Run(ctx context.Context, call ToolCall) (string, error) {
	var a toolArgs
	if len(call.Args) > 0 {
		if err := json.Unmarshal(call.Args, &a); err != nil {
			return "", fmt.Errorf("invalid JSON arguments: %v", err)
		}
	}
	c := t.Cluster
	switch call.Name {
	case "cluster_overview":
		return clip(formatOverview(c.Overview(t.ns(a.Namespace)), t.ns(a.Namespace))), nil

	case "list_resources":
		gvr, err := resolveKind(a.Kind)
		if err != nil {
			return "", err
		}
		ns := t.ns(a.Namespace)
		s, err := c.SnapshotOnce(ctx, gvr, ns, a.Filter)
		if err != nil {
			return "", err
		}
		return clip(formatTable(gvr, ns, s)), nil

	case "get_resource_yaml":
		gvr, err := resolveKind(a.Kind)
		if err != nil {
			return "", err
		}
		y, err := c.YAML(gvr, pathFor(gvr, t.ns(a.Namespace), a.Name))
		if err != nil {
			return "", err
		}
		return clip(RedactManifest(a.Kind, y)), nil

	case "describe_resource":
		gvr, err := resolveKind(a.Kind)
		if err != nil {
			return "", err
		}
		d, err := c.Describe(gvr, pathFor(gvr, t.ns(a.Namespace), a.Name))
		if err != nil {
			return "", err
		}
		// Helm releases embed the rendered manifest, Secrets included
		if head, manifest, ok := strings.Cut(d, "\nMANIFEST:\n"); ok {
			return clip(RedactText(head) + "\nMANIFEST:\n" + RedactManifest("", manifest)), nil
		}
		return clip(RedactText(d)), nil

	case "get_events":
		ev := c.Events(t.ns(a.Namespace), a.Object, a.WarningsOnly, 80)
		if len(ev) == 0 {
			return "no events", nil
		}
		var b strings.Builder
		for _, e := range ev {
			fmt.Fprintf(&b, "%s ago\t%s\t%s/%s\t%s\tx%d\t%s\n", duration.HumanDuration(time.Since(e.Last)), e.Type,
				e.Namespace, e.Object, e.Reason, e.Count, e.Message)
		}
		return clip(RedactText(b.String())), nil

	case "get_logs":
		gvr, err := resolveKind(a.Kind)
		if err != nil {
			return "", err
		}
		tail := a.TailLines
		if tail <= 0 {
			tail = 200
		}
		tail = min(tail, 1000)
		opts := backend.LogOpts{Container: a.Container, AllContainers: a.Container == "", Previous: a.Previous, TailLines: tail}
		lines, err := c.FetchLogs(ctx, gvr, pathFor(gvr, t.ns(a.Namespace), a.Name), opts, 0)
		if err != nil {
			return "", err
		}
		var rx *regexp.Regexp
		if a.Grep != "" {
			if rx, err = regexp.Compile("(?i)" + a.Grep); err != nil {
				rx = regexp.MustCompile("(?i)" + regexp.QuoteMeta(a.Grep))
			}
		}
		var b strings.Builder
		n := 0
		for _, l := range lines {
			if rx != nil && !rx.MatchString(l.Text) {
				continue
			}
			if l.Pod != "" {
				b.WriteString("[" + l.Pod + "] ")
			}
			if l.Container != "" {
				b.WriteString("[" + l.Container + "] ")
			}
			b.WriteString(l.Text + "\n")
			n++
		}
		if n == 0 {
			return "no log lines", nil
		}
		// redact before cutting: a cut PEM block would escape the rule
		out := RedactText(b.String())
		if len(out) > maxToolOutput { // keep the most recent lines
			out = "…[older lines truncated]\n" + out[len(out)-maxToolOutput:]
		}
		return out, nil

	case "xray":
		gvr, err := resolveKind(a.Kind)
		if err != nil {
			return "", err
		}
		if !backend.CanXRay(gvr) {
			return "", fmt.Errorf("xray not supported for %s", gvr)
		}
		root, err := c.XRayOnce(ctx, gvr, t.ns(a.Namespace))
		if err != nil {
			return "", err
		}
		if a.Name != "" {
			if f := findX(root, a.Name); f != nil {
				root = f
			} else if f := findXPrefix(root, a.Name); f != nil {
				root = f
			}
		}
		var b strings.Builder
		dumpX(&b, root, 0)
		return clip(b.String()), nil

	case "lint_cluster":
		var la struct {
			MinSeverity string `json:"min_severity"`
		}
		_ = json.Unmarshal(call.Args, &la)
		r := c.Lint(t.ns(a.Namespace))
		var b strings.Builder
		fmt.Fprintf(&b, "score %d%% (grade %s)\n", r.Score, r.Grade())
		for _, cs := range r.Categories {
			if cs.Scanned > 0 {
				fmt.Fprintf(&b, "%s: %d%% clean of %d (%dE %dW %dI)\n", cs.Name, cs.Percent(), cs.Scanned, cs.Errors, cs.Warns, cs.Infos)
			}
		}
		b.WriteString("findings:\n")
		for _, is := range r.Issues {
			sev := map[backend.Severity]string{backend.SevErr: "ERROR", backend.SevWarn: "WARN", backend.SevInfo: "INFO"}[is.Severity]
			if sev == "INFO" && la.MinSeverity != "info" || sev == "WARN" && la.MinSeverity == "error" {
				continue
			}
			fmt.Fprintf(&b, "%s %s %s: %s\n", sev, is.Rule, is.Path, is.Message)
		}
		return clip(b.String()), nil

	case "list_api_resources":
		var b strings.Builder
		for _, r := range c.Resources() {
			scope := "cluster"
			if r.Namespaced {
				scope = "namespaced"
			}
			fmt.Fprintf(&b, "%s\t%s\t%s\n", r.Kind, r.Name, scope)
		}
		return clip(b.String()), nil

	case "propose_action":
		var p Proposal
		if err := json.Unmarshal(call.Args, &p); err != nil {
			return "", fmt.Errorf("invalid proposal: %v", err)
		}
		gvr, err := resolveKind(p.Kind)
		if err != nil {
			return "", err
		}
		p.GVR = gvr
		if p.Namespace == "" {
			p.Namespace = t.Namespace
		}
		switch p.Action {
		case "scale":
			if p.Replicas == nil {
				return "", fmt.Errorf("scale needs replicas")
			}
		case "apply":
			m, err := checkApplyManifest(&p)
			if err != nil {
				return "", err
			}
			p.Manifest = m
		case "restart", "delete", "cordon", "uncordon":
		default:
			return "", fmt.Errorf("unsupported action %q", p.Action)
		}
		if t.OnPropose != nil {
			t.OnPropose(&p)
		}
		return "Proposal shown to the user. It has NOT been executed; the user will confirm or reject it in the UI.", nil
	}
	return "", fmt.Errorf("unknown tool %q", call.Name)
}

func findX(n *backend.XNode, name string) *backend.XNode {
	if n.Name == name || n.ID == name || strings.HasSuffix(n.ID, "/"+name) {
		if n.Kind != "namespaces" {
			return n
		}
	}
	for _, k := range n.Children {
		if f := findX(k, name); f != nil {
			return f
		}
	}
	return nil
}

func dumpX(b *strings.Builder, n *backend.XNode, depth int) {
	status := n.Status
	if status == "" {
		status = "ok"
	}
	fmt.Fprintf(b, "%s%s %s [%s] %s\n", strings.Repeat("  ", depth), strings.TrimSuffix(n.Kind, "s"), n.ID, status, n.Info)
	for _, k := range n.Children {
		dumpX(b, k, depth+1)
	}
}

func formatTable(gvr, ns string, s *backend.Snapshot) string {
	var b strings.Builder
	if ns == "" {
		ns = "all namespaces"
	}
	fmt.Fprintf(&b, "%s in %s: %d rows\n", gvr, ns, len(s.Rows))
	names := make([]string, len(s.Columns))
	for i, c := range s.Columns {
		names[i] = c.Name
	}
	b.WriteString(strings.Join(names, "\t") + "\n")
	for i, r := range s.Rows {
		if i == 300 {
			fmt.Fprintf(&b, "…%d more rows (use filter)\n", len(s.Rows)-i)
			break
		}
		b.WriteString(strings.Join(r.Cells, "\t") + "\n")
	}
	return b.String()
}

func formatOverview(o backend.Overview, ns string) string {
	var b strings.Builder
	if ns == "" {
		ns = "all"
	}
	fmt.Fprintf(&b, "Namespace scope: %s\nNodes: %d/%d ready\nNamespaces: %d\nPods: %d %v\nDeployments: %d (%d unavailable)\n",
		ns, o.NodesReady, o.Nodes, o.Namespaces, o.Pods, o.PodPhases, o.Deployments, o.DeploymentsBad)
	fmt.Fprintf(&b, "CPU requests: %dm of %dm allocatable\nMemory requests: %dMi of %dMi allocatable\n",
		o.CPURequests, o.CPUCapacity, o.MemRequests>>20, o.MemCapacity>>20)
	if o.CPUUsage >= 0 {
		fmt.Fprintf(&b, "CPU usage: %dm, memory usage: %dMi\n", o.CPUUsage, o.MemUsage>>20)
	} else {
		b.WriteString("Usage metrics: metrics-server not available\n")
	}
	if len(o.Warnings) > 0 {
		b.WriteString("Recent warnings:\n")
		for i, w := range o.Warnings {
			if i == 15 {
				break
			}
			fmt.Fprintf(&b, "- %s ago %s/%s %s x%d: %s\n", duration.HumanDuration(time.Since(w.Last)), w.Namespace, w.Object, w.Reason, w.Count, w.Message)
		}
	}
	return RedactText(b.String())
}

// findXPrefix matches generated names (deployment "web" → pod "web-5b7c…").
func findXPrefix(n *backend.XNode, name string) *backend.XNode {
	if n.Kind != "namespaces" && strings.HasPrefix(n.Name, name+"-") {
		return n
	}
	for _, k := range n.Children {
		if f := findXPrefix(k, name); f != nil {
			return f
		}
	}
	return nil
}

// checkApplyManifest makes sure an apply proposal changes exactly the object
// it names (the card, the confirmation and the diff all use Kind/Name/
// Namespace) and doesn't write redaction placeholders back to the cluster.
// A missing namespace is filled in for namespaced kinds.
func checkApplyManifest(p *Proposal) (string, error) {
	if strings.TrimSpace(p.Manifest) == "" {
		return "", fmt.Errorf("apply needs the full manifest")
	}
	if strings.Contains(p.Manifest, redacted) {
		return "", fmt.Errorf("the manifest contains %s placeholders: they would overwrite the real values. "+
			"Omit those fields or ask the user for the values", redacted)
	}
	if len(docSep.Split(strings.TrimSpace(p.Manifest), -1)) > 1 {
		return "", fmt.Errorf("apply takes a single object: propose one apply per object")
	}
	var obj map[string]any
	if err := yaml.Unmarshal([]byte(p.Manifest), &obj); err != nil || obj == nil {
		return "", fmt.Errorf("invalid manifest: %v", err)
	}
	md, _ := obj["metadata"].(map[string]any)
	if md == nil {
		return "", fmt.Errorf("manifest has no metadata")
	}
	if name := str(md["name"]); name != p.Name {
		return "", fmt.Errorf("manifest name %q differs from the proposal name %q", name, p.Name)
	}
	kind := str(obj["kind"])
	gvr, err := resolveKind(kind)
	if err != nil { // registry not loaded: Kind → plural resource of the GVR
		gvr = p.GVR
		if !strings.HasSuffix(p.GVR, "/"+strings.ToLower(kind)+"s") {
			gvr = ""
		}
	}
	if kind == "" || gvr != p.GVR {
		return "", fmt.Errorf("manifest kind %q differs from the proposal kind %q", str(obj["kind"]), p.Kind)
	}
	if backend.IsNamespaced(p.GVR) {
		switch ns := str(md["namespace"]); {
		case ns == "":
			md["namespace"] = p.Namespace
		case ns != p.Namespace:
			return "", fmt.Errorf("manifest namespace %q differs from the proposal namespace %q", ns, p.Namespace)
		}
	}
	b, err := yaml.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
