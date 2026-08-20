package monitoring

import (
	"context"
	"strings"
	"unicode"
)

// SeriesStatus is the verdict CheckRuleSeries reaches for one rule.
type SeriesStatus string

const (
	// SeriesFound: at least one candidate metric name extracted from the
	// rule's query currently has series in Prometheus.
	SeriesFound SeriesStatus = "found"
	// SeriesMissing: candidate metric names were extracted and none of them
	// currently has series — the JDWLABS-335 shape, a rule reading a metric
	// nothing emits.
	SeriesMissing SeriesStatus = "missing"
	// SeriesUnknown: no candidate metric name could be extracted from the
	// query, so nothing was checked. This is not "found" — an unreadable
	// query says nothing about whether its series exists, so it must not be
	// reported as evidence that it does.
	SeriesUnknown SeriesStatus = "unknown"
)

// promqlKeyword covers the vector-matching and aggregation modifiers that are
// never metric names, plus binary/set operators spelled as words.
var promqlKeyword = map[string]bool{
	"by": true, "without": true, "on": true, "ignoring": true,
	"group_left": true, "group_right": true, "bool": true, "offset": true,
	"and": true, "or": true, "unless": true,
}

// promqlFunction is the built-in PromQL function set as of Prometheus 3.x
// (github.com/prometheus/prometheus/promql/parser/functions.go). A token
// immediately followed by "(" is already excluded by the caller, so this list
// exists only to catch functions used bare, e.g. inside "()" grouping.
var promqlFunction = map[string]bool{
	"abs": true, "absent": true, "absent_over_time": true, "acos": true, "acosh": true,
	"asin": true, "asinh": true, "atan": true, "atanh": true, "avg_over_time": true,
	"ceil": true, "changes": true, "clamp": true, "clamp_max": true, "clamp_min": true,
	"cos": true, "cosh": true, "count_over_time": true, "days_in_month": true,
	"day_of_month": true, "day_of_week": true, "day_of_year": true, "deg": true,
	"delta": true, "deriv": true, "exp": true, "floor": true, "histogram_avg": true,
	"histogram_count": true, "histogram_fraction": true, "histogram_quantile": true,
	"histogram_stddev": true, "histogram_stdvar": true, "histogram_sum": true,
	"hour": true, "idelta": true, "increase": true, "irate": true, "label_join": true,
	"label_replace": true, "last_over_time": true, "ln": true, "log10": true, "log2": true,
	"max_over_time": true, "min_over_time": true, "minute": true, "month": true,
	"pi": true, "predict_linear": true, "quantile_over_time": true, "rad": true,
	"rate": true, "resets": true, "round": true, "scalar": true, "sgn": true,
	"sin": true, "sinh": true, "sort": true, "sort_desc": true, "sqrt": true,
	"stddev_over_time": true, "stdvar_over_time": true, "sum_over_time": true,
	"tan": true, "tanh": true, "time": true, "timestamp": true, "vector": true,
	"year": true,
	// Aggregation operators — not functions, but bare identifiers followed by
	// "(" the same way, so they need the same exclusion.
	"sum": true, "min": true, "max": true, "avg": true, "group": true,
	"stddev": true, "stdvar": true, "count": true, "count_values": true,
	"bottomk": true, "topk": true, "quantile": true,
}

// CandidateMetricNames extracts the identifiers in a PromQL expression that
// could plausibly be metric names. It is a single left-to-right scan, not a
// parser, and excludes a token when any of the following holds:
//
//   - it is a known PromQL keyword or built-in function/aggregator name
//   - it starts with "__" (a reserved label, e.g. __name__)
//   - it appears inside a "{...}" matcher, where every identifier is a label
//     name — a vector selector's own metric name is always written before
//     the brace, never inside it
//   - it appears inside the parenthesised list after by/without/on/ignoring/
//     group_left/group_right, which names labels, not metrics
//   - it is immediately followed by "(", meaning it is itself a call this
//     package's function/aggregator list did not know about
//   - it is immediately glued to a preceding digit, e.g. the "m" in "[5m]" —
//     a duration or offset unit, never a bare identifier there
//   - it is inside a quoted string, which this scan skips over entirely
//     rather than tokenising, because a label's string value or a
//     label_replace() argument is not a vector selector
//
// This is deliberately not a full PromQL parser — that would be a heavyweight
// dependency for one read-only inspection command. Its remaining blind spots
// (an aggregator or function this package's lists do not yet know, an
// unusual binary-expression shape) fail toward over-inclusion, and
// CheckRuleSeries only reports SeriesMissing when every extracted candidate
// comes back empty — so a spurious extra candidate can only push a verdict
// toward SeriesFound, never invent a false SeriesMissing.
func CandidateMetricNames(expr string) []string {
	runes := []rune(expr)
	var names []string
	seen := map[string]bool{}

	braceDepth := 0
	var parenIsLabelList []bool
	lastIdent := ""
	lastIdentEnd := -1

	i := 0
	for i < len(runes) {
		r := runes[i]
		switch {
		case r == '"' || r == '\'' || r == '`':
			i = skipQuotedString(runes, i)
		case r == '{':
			braceDepth++
			i++
		case r == '}':
			if braceDepth > 0 {
				braceDepth--
			}
			i++
		case r == '(':
			isLabelList := lastIdentEnd >= 0 &&
				onlySpaceBetween(runes, lastIdentEnd, i) &&
				promqlLabelListKeyword[strings.ToLower(lastIdent)]
			parenIsLabelList = append(parenIsLabelList, isLabelList)
			i++
		case r == ')':
			if len(parenIsLabelList) > 0 {
				parenIsLabelList = parenIsLabelList[:len(parenIsLabelList)-1]
			}
			i++
		case isIdentStart(r):
			start := i
			i++
			for i < len(runes) && isIdentPart(runes[i]) {
				i++
			}
			tok := string(runes[start:i])
			lastIdent, lastIdentEnd = tok, i

			lower := strings.ToLower(tok)
			inLabelList := len(parenIsLabelList) > 0 && parenIsLabelList[len(parenIsLabelList)-1]
			switch {
			case promqlKeyword[lower] || promqlFunction[lower]:
			case strings.HasPrefix(tok, "__"):
			case braceDepth > 0:
			case inLabelList:
			case start > 0 && unicode.IsDigit(runes[start-1]):
			case followedByOpenParen(runes, i):
			case !seen[tok]:
				seen[tok] = true
				names = append(names, tok)
			}
		default:
			i++
		}
	}
	return names
}

func isIdentStart(r rune) bool { return r == '_' || r == ':' || unicode.IsLetter(r) }
func isIdentPart(r rune) bool {
	return r == '_' || r == ':' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// promqlLabelListKeyword marks the operators whose parenthesised argument is
// a label list rather than an expression.
var promqlLabelListKeyword = map[string]bool{
	"by": true, "without": true, "on": true, "ignoring": true,
	"group_left": true, "group_right": true,
}

func onlySpaceBetween(runes []rune, from, to int) bool {
	for _, r := range runes[from:to] {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func followedByOpenParen(runes []rune, from int) bool {
	i := from
	for i < len(runes) && unicode.IsSpace(runes[i]) {
		i++
	}
	return i < len(runes) && runes[i] == '('
}

// skipQuotedString returns the index just past the string literal starting at
// runes[start], honouring backslash escapes so an escaped quote does not end
// the string early. An unterminated string (a malformed expression, which a
// live rule from a validating admission webhook should never produce) skips
// to the end rather than looping.
func skipQuotedString(runes []rune, start int) int {
	quote := runes[start]
	i := start + 1
	for i < len(runes) {
		if runes[i] == '\\' && i+1 < len(runes) {
			i += 2
			continue
		}
		if runes[i] == quote {
			return i + 1
		}
		i++
	}
	return i
}

// SeriesChecker is the subset of PrometheusClient CheckRuleSeries needs, so
// tests can supply a stub instead of an HTTP server.
type SeriesChecker interface {
	SeriesExists(ctx context.Context, metric string) (bool, error)
}

// CheckRuleSeries extracts candidate metric names from a rule's query and
// reports whether Prometheus currently has series for at least one of them.
// The first error from SeriesExists aborts the check and is returned as-is —
// a query failure must not be read as "missing", which would misreport a
// Prometheus outage as a coverage gap.
func CheckRuleSeries(ctx context.Context, pc SeriesChecker, query string) (SeriesStatus, error) {
	candidates := CandidateMetricNames(query)
	if len(candidates) == 0 {
		return SeriesUnknown, nil
	}
	for _, name := range candidates {
		found, err := pc.SeriesExists(ctx, name)
		if err != nil {
			return "", err
		}
		if found {
			return SeriesFound, nil
		}
	}
	return SeriesMissing, nil
}
