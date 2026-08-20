package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jdwlabs/platform/internal/bootstrap"
	"github.com/jdwlabs/platform/internal/display"
	"github.com/jdwlabs/platform/internal/k8s"
	"github.com/jdwlabs/platform/internal/monitoring"
)

const (
	monitoringNamespace = "monitoring"

	prometheusPodSelector = "app.kubernetes.io/name=prometheus"
	prometheusPort        = 9090
	prometheusDefaultAddr = "http://platform-kube-prometheus-s-prometheus.monitoring.svc:9090"
	prometheusAddrEnv     = "PLATFORMCTL_PROMETHEUS_ADDR"

	alertmanagerPodSelector = "app.kubernetes.io/name=alertmanager"
	alertmanagerPort        = 9093
	alertmanagerDefaultAddr = "http://platform-kube-prometheus-s-alertmanager.monitoring.svc:9093"
	alertmanagerAddrEnv     = "PLATFORMCTL_ALERTMANAGER_ADDR"
)

type alertsGlobals struct {
	PrometheusAddr   string
	AlertmanagerAddr string
}

func newClusterAlertsCmd(g *Globals) *cobra.Command {
	shared := &alertsGlobals{}

	cmd := &cobra.Command{
		Use:   "alerts",
		Short: "Inspect live alerting state: active alerts, scrape target health, rule coverage",
		Long: `Read-only inspection of the monitoring stack's live state, over Prometheus's
and Alertmanager's own HTTP APIs.

"cluster status" only asserts that the alertmanager-config Secret exists —
that says nothing about whether an alert fires, where it routes, or whether
the metric a rule references is ever scraped at all. These three commands are
the path to that live state that AGENTS.md's binary contract needed and did
not have:

  alerts list     active Alertmanager alerts and the receivers each reaches
  alerts targets  Prometheus scrape target health and last-scrape time
  alerts rules    alerting rule state, and whether its metric has any series

Both APIs are reached the same way "gitsync" reaches Grafana's: an in-cluster
.svc address is not resolvable from a workstation, so it is reached through an
automatic port-forward to the Prometheus or Alertmanager pod.`,
	}
	cmd.PersistentFlags().StringVar(&shared.PrometheusAddr, "prometheus-addr", "",
		"Prometheus base URL; an in-cluster .svc address is reached by an automatic port-forward (env "+prometheusAddrEnv+")")
	cmd.PersistentFlags().StringVar(&shared.AlertmanagerAddr, "alertmanager-addr", "",
		"Alertmanager base URL; an in-cluster .svc address is reached by an automatic port-forward (env "+alertmanagerAddrEnv+")")

	cmd.AddCommand(newAlertsListCmd(g, shared))
	cmd.AddCommand(newAlertsTargetsCmd(g, shared))
	cmd.AddCommand(newAlertsRulesCmd(g, shared))
	return cmd
}

// ---------------------------------------------------------------------------
// alerts list
// ---------------------------------------------------------------------------

var alertFields = []string{"alertname", "severity", "state", "receivers", "startsAt", "summary"}

const alertsDefaultFields = "alertname,severity,receivers,startsAt"

type alertsListOptions struct {
	Fields           string
	Full             bool
	Receiver         string
	IncludeSilenced  bool
	IncludeInhibited bool
}

func newAlertsListCmd(g *Globals, shared *alertsGlobals) *cobra.Command {
	opts := &alertsListOptions{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List active alerts with the receivers each one reaches",
		Long: `List the alerts Alertmanager currently holds, with the receivers its routing
tree resolved each one to — the acceptance evidence "this alert reaches these
receivers" needs, produced from live routing state rather than an offline
resolution of the config tree.

Excludes silenced and inhibited alerts by default, matching Alertmanager's own
default view; --include-silenced and --include-inhibited widen it.`,
		Example: `  platformctl cluster alerts list
  platformctl cluster alerts list --receiver discord
  platformctl cluster alerts list --full`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAlertsList(cmd, g, shared, opts)
		},
	}
	cmd.Flags().StringVar(&opts.Fields, "fields", "",
		"comma-separated fields to report instead of "+alertsDefaultFields)
	cmd.Flags().BoolVar(&opts.Full, "full", false, "report every field")
	cmd.Flags().StringVar(&opts.Receiver, "receiver", "", "restrict to alerts routed to one receiver")
	cmd.Flags().BoolVar(&opts.IncludeSilenced, "include-silenced", false, "also report silenced alerts")
	cmd.Flags().BoolVar(&opts.IncludeInhibited, "include-inhibited", false, "also report inhibited alerts")
	return cmd
}

func runAlertsList(cmd *cobra.Command, g *Globals, shared *alertsGlobals, opts *alertsListOptions) error {
	out := cmd.OutOrStdout()

	fields, err := resolveFields("alert", alertFields, alertsDefaultFields, opts.Fields, opts.Full)
	if err != nil {
		return reportCLIError(out, err, "Run `platformctl cluster alerts list --help` for the field list")
	}

	ac, stop, err := newAlertmanagerClient(cmd.Context(), shared)
	defer stop()
	if err != nil {
		return reportCLIError(out, err, "Set "+alertmanagerAddrEnv+" to reach Alertmanager directly, or check KUBECONFIG")
	}

	alerts, err := ac.Alerts(cmd.Context(), monitoring.AlertOptions{
		Receiver:         opts.Receiver,
		IncludeSilenced:  opts.IncludeSilenced,
		IncludeInhibited: opts.IncludeInhibited,
	})
	if err != nil {
		return reportCLIError(out, err, "Check the alertmanager-config Secret with `platformctl cluster status`")
	}
	sort.Slice(alerts, func(i, j int) bool { return alerts[i].Name < alerts[j].Name })

	summary := fmt.Sprintf("%d active alert(s)", len(alerts))

	if g.JSON {
		em := NewEmitter(out, g.JSON)
		if g.Session != nil {
			em.SetSession(g.Session)
		}
		for _, a := range alerts {
			detail := map[string]string{}
			for _, f := range fields {
				detail[f] = alertFieldValue(a, f)
			}
			status := "info"
			if len(a.Receivers) == 0 {
				status = "broken"
			}
			em.Emit(Event{Phase: "alerts", Name: a.Name, Status: status,
				Message: fmt.Sprintf("state=%s receivers=%s", a.State, strings.Join(a.Receivers, "|")), Detail: detail})
		}
		em.Emit(Event{Phase: "alerts", Name: "list", Status: "ok", Message: summary})
		return nil
	}

	if err := display.ToonScalar(out, "count", summary); err != nil {
		return err
	}
	if err := display.ToonTable(out, "alerts", fields, alertRows(alerts, fields)); err != nil {
		return err
	}
	if len(alerts) == 0 {
		return display.ToonScalar(out, "result", "no active alerts")
	}
	return display.ToonList(out, "help", []string{
		"Run `platformctl cluster alerts list --full` for every field",
	})
}

func alertRows(alerts []monitoring.Alert, fields []string) [][]string {
	rows := make([][]string, 0, len(alerts))
	for _, a := range alerts {
		row := make([]string, 0, len(fields))
		for _, f := range fields {
			row = append(row, alertFieldValue(a, f))
		}
		rows = append(rows, row)
	}
	return rows
}

func alertFieldValue(a monitoring.Alert, field string) string {
	switch field {
	case "alertname":
		return a.Name
	case "severity":
		return a.Labels["severity"]
	case "state":
		return a.State
	case "receivers":
		if len(a.Receivers) == 0 {
			return "(none)"
		}
		return strings.Join(a.Receivers, "|")
	case "startsAt":
		return a.StartsAt
	case "summary":
		return a.Annotations["summary"]
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// alerts targets
// ---------------------------------------------------------------------------

var targetFields = []string{"job", "instance", "health", "lastScrape", "lastScrapeDuration", "samplesScraped", "lastError", "scrapeUrl"}

const targetsDefaultFields = "job,instance,health,lastScrape"

type alertsTargetsOptions struct {
	Fields           string
	Full             bool
	Job              string
	SkipSamplesCheck bool
}

func newAlertsTargetsCmd(g *Globals, shared *alertsGlobals) *cobra.Command {
	opts := &alertsTargetsOptions{}
	cmd := &cobra.Command{
		Use:   "targets",
		Short: "Report scrape target health, last-scrape time, and samples scraped (exits non-zero on any unhealthy target)",
		Long: `Report every active Prometheus scrape target with its health, last-scrape
timestamp, and (unless --skip-samples-check) the samples its last scrape
actually parsed.

Health alone is not the check: a target can report Up while Prometheus
serves an empty response body from it, and "Up" says nothing about that.
samplesScraped reads the scrape_samples_scraped
meta-metric per target — a target reporting Up with zero samples is exactly
that shape, so it is treated as unhealthy alongside a target reporting Down.

Exits non-zero when any reported target is unhealthy.`,
		Example: `  platformctl cluster alerts targets
  platformctl cluster alerts targets --job truenas
  platformctl cluster alerts targets --skip-samples-check`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAlertsTargets(cmd, g, shared, opts)
		},
	}
	cmd.Flags().StringVar(&opts.Fields, "fields", "",
		"comma-separated fields to report instead of "+targetsDefaultFields)
	cmd.Flags().BoolVar(&opts.Full, "full", false, "report every field")
	cmd.Flags().StringVar(&opts.Job, "job", "", "restrict to one scrape job")
	cmd.Flags().BoolVar(&opts.SkipSamplesCheck, "skip-samples-check", false,
		"skip the per-target scrape_samples_scraped query; health becomes the only check")
	return cmd
}

// targetReport is a Target plus the derived fields the command reports —
// SamplesScraped is looked up separately from Targets, so it lives here
// rather than on the monitoring.Target the client returns.
type targetReport struct {
	monitoring.Target
	SamplesScraped      int64
	SamplesCheckSkipped bool
}

func (t targetReport) healthy() bool {
	if t.Health != "up" {
		return false
	}
	if t.SamplesCheckSkipped {
		return true
	}
	return t.SamplesScraped > 0
}

func runAlertsTargets(cmd *cobra.Command, g *Globals, shared *alertsGlobals, opts *alertsTargetsOptions) error {
	out := cmd.OutOrStdout()

	fields, err := resolveFields("target", targetFields, targetsDefaultFields, opts.Fields, opts.Full)
	if err != nil {
		return reportCLIError(out, err, "Run `platformctl cluster alerts targets --help` for the field list")
	}

	pc, stop, err := newPrometheusClient(cmd.Context(), shared)
	defer stop()
	if err != nil {
		return reportCLIError(out, err, "Set "+prometheusAddrEnv+" to reach Prometheus directly, or check KUBECONFIG")
	}

	targets, err := pc.Targets(cmd.Context())
	if err != nil {
		return reportCLIError(out, err, "Check the kube-prometheus-stack Application with `platformctl cluster status`")
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Job != targets[j].Job {
			return targets[i].Job < targets[j].Job
		}
		return targets[i].Instance < targets[j].Instance
	})

	shown := make([]targetReport, 0, len(targets))
	for _, t := range targets {
		if opts.Job != "" && t.Job != opts.Job {
			continue
		}
		tr := targetReport{Target: t, SamplesCheckSkipped: opts.SkipSamplesCheck}
		if !opts.SkipSamplesCheck && t.Health == "up" {
			n, err := pc.SamplesScraped(cmd.Context(), t.Job, t.Instance)
			if err != nil {
				return reportCLIError(out, fmt.Errorf("read scrape_samples_scraped for %s/%s: %w", t.Job, t.Instance, err),
					"Pass --skip-samples-check to report health/lastScrape only")
			}
			tr.SamplesScraped = n
		}
		shown = append(shown, tr)
	}

	var unhealthy []targetReport
	for _, t := range shown {
		if !t.healthy() {
			unhealthy = append(unhealthy, t)
		}
	}
	summary := fmt.Sprintf("%d target(s), %d unhealthy", len(shown), len(unhealthy))

	if g.JSON {
		if err := emitTargetEvents(out, g, shown, fields, summary); err != nil {
			return err
		}
	} else {
		if err := display.ToonScalar(out, "count", summary); err != nil {
			return err
		}
		if err := display.ToonTable(out, "targets", fields, targetRows(shown, fields)); err != nil {
			return err
		}
		if len(unhealthy) == 0 {
			if err := display.ToonScalar(out, "result", "every reported target is healthy"); err != nil {
				return err
			}
		} else {
			help := []string{"Run `platformctl cluster alerts targets --full` for lastError and scrapeUrl"}
			if err := display.ToonList(out, "help", help); err != nil {
				return err
			}
		}
	}

	if len(unhealthy) > 0 {
		names := make([]string, 0, len(unhealthy))
		for _, t := range unhealthy {
			names = append(names, t.Job+"/"+t.Instance)
		}
		return fmt.Errorf("%d target(s) unhealthy: %s", len(unhealthy), strings.Join(names, ", "))
	}
	return nil
}

func targetRows(targets []targetReport, fields []string) [][]string {
	rows := make([][]string, 0, len(targets))
	for _, t := range targets {
		row := make([]string, 0, len(fields))
		for _, f := range fields {
			row = append(row, targetFieldValue(t, f))
		}
		rows = append(rows, row)
	}
	return rows
}

func targetFieldValue(t targetReport, field string) string {
	switch field {
	case "job":
		return t.Job
	case "instance":
		return t.Instance
	case "health":
		return t.Health
	case "lastScrape":
		if t.LastScrape == "" {
			return "never"
		}
		return t.LastScrape
	case "lastScrapeDuration":
		return strconv.FormatFloat(t.LastScrapeDuration, 'f', 3, 64)
	case "samplesScraped":
		if t.SamplesCheckSkipped {
			return "skipped"
		}
		if t.Health != "up" {
			return "n/a"
		}
		return strconv.FormatInt(t.SamplesScraped, 10)
	case "lastError":
		return t.LastError
	case "scrapeUrl":
		return t.ScrapeURL
	default:
		return ""
	}
}

func emitTargetEvents(out io.Writer, g *Globals, targets []targetReport, fields []string, summary string) error {
	em := NewEmitter(out, g.JSON)
	if g.Session != nil {
		em.SetSession(g.Session)
	}
	for _, t := range targets {
		detail := map[string]string{}
		for _, f := range fields {
			detail[f] = targetFieldValue(t, f)
		}
		status := "ok"
		if !t.healthy() {
			status = "broken"
		}
		em.Emit(Event{Phase: "alerts-targets", Name: t.Job + "/" + t.Instance, Status: status,
			Message: fmt.Sprintf("health=%s lastScrape=%s", t.Health, t.LastScrape), Detail: detail})
	}
	em.Emit(Event{Phase: "alerts-targets", Name: "summary", Status: "ok", Message: summary})
	return nil
}

// ---------------------------------------------------------------------------
// alerts rules
// ---------------------------------------------------------------------------

var ruleFields = []string{"group", "name", "state", "health", "seriesStatus", "query", "lastError"}

const rulesDefaultFields = "group,name,state,seriesStatus"

type alertsRulesOptions struct {
	Fields          string
	Full            bool
	Group           string
	SkipSeriesCheck bool
}

func newAlertsRulesCmd(g *Globals, shared *alertsGlobals) *cobra.Command {
	opts := &alertsRulesOptions{}
	cmd := &cobra.Command{
		Use:   "rules",
		Short: "Report alerting rule state and whether each rule's metric has any series (exits non-zero on a broken rule or a missing series)",
		Long: `Report every alerting rule (recording rules are excluded — they have no
receiver and no firing state) with its current state and, unless
--skip-series-check, whether Prometheus currently has any series for the
metric the rule's query names.

A rule can read cleanly and still be worthless: a rule referencing a metric
nothing emits stays permanently inactive and reads as coverage, which is worse
than no rule at all. seriesStatus is one of:

  found    at least one metric name extracted from the query has series now
  missing  metric names were extracted and none of them has series
  unknown  no metric name could be extracted from the query — this is a
           limit of the extraction, not a claim that the series is fine, and
           it is excluded from the exit-code gate for exactly that reason

The extraction is a scan of the query text, not a PromQL parser, and can miss
an unusual expression shape; see internal/monitoring.CandidateMetricNames.

Exits non-zero when any rule's health is "err" or its seriesStatus is
"missing".`,
		Example: `  platformctl cluster alerts rules
  platformctl cluster alerts rules --group truenas.rules
  platformctl cluster alerts rules --skip-series-check`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAlertsRules(cmd, g, shared, opts)
		},
	}
	cmd.Flags().StringVar(&opts.Fields, "fields", "",
		"comma-separated fields to report instead of "+rulesDefaultFields)
	cmd.Flags().BoolVar(&opts.Full, "full", false, "report every field, including the raw query")
	cmd.Flags().StringVar(&opts.Group, "group", "", "restrict to one rule group")
	cmd.Flags().BoolVar(&opts.SkipSeriesCheck, "skip-series-check", false,
		"skip the per-rule series-existence query; report rule state only")
	return cmd
}

type ruleReport struct {
	monitoring.Rule
	SeriesStatus monitoring.SeriesStatus
}

func runAlertsRules(cmd *cobra.Command, g *Globals, shared *alertsGlobals, opts *alertsRulesOptions) error {
	out := cmd.OutOrStdout()

	fields, err := resolveFields("rule", ruleFields, rulesDefaultFields, opts.Fields, opts.Full)
	if err != nil {
		return reportCLIError(out, err, "Run `platformctl cluster alerts rules --help` for the field list")
	}

	pc, stop, err := newPrometheusClient(cmd.Context(), shared)
	defer stop()
	if err != nil {
		return reportCLIError(out, err, "Set "+prometheusAddrEnv+" to reach Prometheus directly, or check KUBECONFIG")
	}

	rules, err := pc.Rules(cmd.Context())
	if err != nil {
		return reportCLIError(out, err, "Check the kube-prometheus-stack Application with `platformctl cluster status`")
	}

	shown := make([]ruleReport, 0, len(rules))
	for _, r := range rules {
		if opts.Group != "" && r.Group != opts.Group {
			continue
		}
		rr := ruleReport{Rule: r, SeriesStatus: monitoring.SeriesUnknown}
		if !opts.SkipSeriesCheck {
			status, err := monitoring.CheckRuleSeries(cmd.Context(), pc, r.Query)
			if err != nil {
				return reportCLIError(out, fmt.Errorf("check series for rule %s/%s: %w", r.Group, r.Name, err),
					"Pass --skip-series-check to report rule state only")
			}
			rr.SeriesStatus = status
		}
		shown = append(shown, rr)
	}
	sort.Slice(shown, func(i, j int) bool {
		if shown[i].Group != shown[j].Group {
			return shown[i].Group < shown[j].Group
		}
		return shown[i].Name < shown[j].Name
	})

	var broken []ruleReport
	for _, r := range shown {
		if r.Health == "err" || r.SeriesStatus == monitoring.SeriesMissing {
			broken = append(broken, r)
		}
	}
	summary := fmt.Sprintf("%d alerting rule(s), %d broken", len(shown), len(broken))

	if g.JSON {
		if err := emitRuleEvents(out, g, shown, fields, summary); err != nil {
			return err
		}
	} else {
		if err := display.ToonScalar(out, "count", summary); err != nil {
			return err
		}
		if err := display.ToonTable(out, "rules", fields, ruleRows(shown, fields)); err != nil {
			return err
		}
		if len(broken) == 0 {
			if err := display.ToonScalar(out, "result", "no broken rule health and no missing series"); err != nil {
				return err
			}
		} else {
			help := []string{"Run `platformctl cluster alerts rules --full` for the raw query"}
			if err := display.ToonList(out, "help", help); err != nil {
				return err
			}
		}
	}

	if len(broken) > 0 {
		names := make([]string, 0, len(broken))
		for _, r := range broken {
			names = append(names, r.Group+"/"+r.Name)
		}
		return fmt.Errorf("%d rule(s) broken: %s", len(broken), strings.Join(names, ", "))
	}
	return nil
}

func ruleRows(rules []ruleReport, fields []string) [][]string {
	rows := make([][]string, 0, len(rules))
	for _, r := range rules {
		row := make([]string, 0, len(fields))
		for _, f := range fields {
			row = append(row, ruleFieldValue(r, f))
		}
		rows = append(rows, row)
	}
	return rows
}

func ruleFieldValue(r ruleReport, field string) string {
	switch field {
	case "group":
		return r.Group
	case "name":
		return r.Name
	case "state":
		return r.State
	case "health":
		return r.Health
	case "seriesStatus":
		return string(r.SeriesStatus)
	case "query":
		return r.Query
	case "lastError":
		return r.LastError
	default:
		return ""
	}
}

func emitRuleEvents(out io.Writer, g *Globals, rules []ruleReport, fields []string, summary string) error {
	em := NewEmitter(out, g.JSON)
	if g.Session != nil {
		em.SetSession(g.Session)
	}
	for _, r := range rules {
		detail := map[string]string{}
		for _, f := range fields {
			detail[f] = ruleFieldValue(r, f)
		}
		status := "ok"
		if r.Health == "err" || r.SeriesStatus == monitoring.SeriesMissing {
			status = "broken"
		}
		em.Emit(Event{Phase: "alerts-rules", Name: r.Group + "/" + r.Name, Status: status,
			Message: fmt.Sprintf("state=%s health=%s series=%s", r.State, r.Health, r.SeriesStatus), Detail: detail})
	}
	em.Emit(Event{Phase: "alerts-rules", Name: "summary", Status: "ok", Message: summary})
	return nil
}

// ---------------------------------------------------------------------------
// shared: field resolution, client construction
// ---------------------------------------------------------------------------

// resolveFields is the --fields/--full resolver every "cluster alerts"
// subcommand shares. It differs from volumes.go's resolveVolumeFields only in
// taking the field set and default as parameters, since three subcommands
// here each have their own.
func resolveFields(kind string, all []string, defaultCSV, csv string, full bool) ([]string, error) {
	if full && csv != "" {
		return nil, fmt.Errorf("--fields and --full are mutually exclusive")
	}
	if full {
		return all, nil
	}
	if csv == "" {
		return strings.Split(defaultCSV, ","), nil
	}
	known := map[string]bool{}
	for _, f := range all {
		known[f] = true
	}
	parts := strings.Split(csv, ",")
	fields := make([]string, 0, len(parts))
	for _, p := range parts {
		f := strings.TrimSpace(p)
		if f == "" {
			continue
		}
		if !known[f] {
			return nil, fmt.Errorf("unknown %s field %s; valid: %s", kind, f, strings.Join(all, ", "))
		}
		fields = append(fields, f)
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("--fields is empty; valid: %s", strings.Join(all, ", "))
	}
	return fields, nil
}

// newPrometheusClient and newAlertmanagerClient mirror newGitSyncClient: an
// in-cluster .svc address does not resolve from a workstation, so it is
// reached through an automatic port-forward to the pod. Neither API needs
// credentials — unlike Grafana's, they are not fronted by auth internally.

func newPrometheusClient(ctx context.Context, shared *alertsGlobals) (*monitoring.PrometheusClient, func(), error) {
	addr, stop, err := resolveMonitoringAddr(ctx, shared.PrometheusAddr, prometheusAddrEnv, prometheusDefaultAddr,
		monitoringNamespace, prometheusPodSelector, prometheusPort)
	if err != nil {
		return nil, stop, err
	}
	return monitoring.NewPrometheusClient(addr), stop, nil
}

func newAlertmanagerClient(ctx context.Context, shared *alertsGlobals) (*monitoring.AlertmanagerClient, func(), error) {
	addr, stop, err := resolveMonitoringAddr(ctx, shared.AlertmanagerAddr, alertmanagerAddrEnv, alertmanagerDefaultAddr,
		monitoringNamespace, alertmanagerPodSelector, alertmanagerPort)
	if err != nil {
		return nil, stop, err
	}
	return monitoring.NewAlertmanagerClient(addr), stop, nil
}

func resolveMonitoringAddr(ctx context.Context, flagAddr, envVar, defaultAddr, namespace, podSelector string, port int) (string, func(), error) {
	noop := func() {}

	addr := flagAddr
	if addr == "" {
		addr = os.Getenv(envVar)
	}
	if addr == "" {
		addr = defaultAddr
	}
	if !strings.Contains(addr, ".svc") {
		return addr, noop, nil
	}

	kc, err := volumeKubeClient()
	if err != nil {
		return "", noop, err
	}
	restCfg, err := k8s.NewRestConfig()
	if err != nil {
		return "", noop, fmt.Errorf("rest config: %w", err)
	}
	local, cancel, err := bootstrap.StartPortForward(ctx, restCfg, kc, namespace, podSelector, port)
	if err != nil {
		return "", noop, fmt.Errorf("auto port-forward %s: %w", podSelector, err)
	}
	return local, cancel, nil
}
