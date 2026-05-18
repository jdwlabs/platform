package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// RunChecks executes all checks concurrently within each layer (layers run
// sequentially so later layers can surface context about earlier failures).
func RunChecks(ctx context.Context, checks []Check) []LayerResult {
	// Group checks by layer, preserving order within each layer.
	type entry struct {
		idx   int
		check Check
	}
	layerEntries := map[int][]entry{}
	layerOrder := []int{}
	seen := map[int]bool{}
	for i, c := range checks {
		if !seen[c.Layer] {
			seen[c.Layer] = true
			layerOrder = append(layerOrder, c.Layer)
		}
		layerEntries[c.Layer] = append(layerEntries[c.Layer], entry{i, c})
	}

	var results []LayerResult
	for _, layer := range layerOrder {
		entries := layerEntries[layer]
		crs := make([]CheckResult, len(entries))
		var wg sync.WaitGroup
		for i, e := range entries {
			wg.Add(1)
			go func(i int, e entry) {
				defer wg.Done()
				crs[i] = CheckResult{Check: e.check, Result: e.check.Run(ctx)}
			}(i, e)
		}
		wg.Wait()
		results = append(results, LayerResult{
			Layer:  layer,
			Group:  entries[0].check.Group,
			Checks: crs,
		})
	}
	return results
}

// OverallStatus returns the worst status across all layers.
func OverallStatus(layers []LayerResult) Status {
	worst := StatusPass
	for _, l := range layers {
		if s := l.OverallStatus(); s > worst {
			worst = s
		}
	}
	return worst
}

// PrintResults writes a human-readable layered report to w.
func PrintResults(w io.Writer, layers []LayerResult, noColor bool) {
	const (
		reset  = "\033[0m"
		bold   = "\033[1m"
		green  = "\033[32m"
		yellow = "\033[33m"
		red    = "\033[31m"
		cyan   = "\033[36m"
	)
	cc := func(s, code string) string {
		if noColor {
			return s
		}
		return code + s + reset
	}

	for _, layer := range layers {
		header := fmt.Sprintf("Layer %d — %s", layer.Layer, layer.Group)
		fmt.Fprintln(w, cc(header, bold+cyan))
		for _, cr := range layer.Checks {
			glyph := cr.Result.Status.Glyph()
			switch cr.Result.Status {
			case StatusPass:
				glyph = cc(glyph, green)
			case StatusWarn:
				glyph = cc(glyph, yellow)
			case StatusFail, StatusUnknown:
				glyph = cc(glyph, red)
			}
			fmt.Fprintf(w, "  %s  %-42s %s\n", glyph, cr.Check.Name, cr.Result.Message)
		}
		fmt.Fprintln(w)
	}

	var total, failing, warning int
	for _, l := range layers {
		for _, cr := range l.Checks {
			total++
			switch cr.Result.Status {
			case StatusFail, StatusUnknown:
				failing++
			case StatusWarn:
				warning++
			}
		}
	}
	passing := total - failing - warning
	summary := fmt.Sprintf("%d/%d checks passing", passing, total)
	if failing > 0 {
		summary += fmt.Sprintf(", %d failing", failing)
	}
	if warning > 0 {
		summary += fmt.Sprintf(", %d warning(s)", warning)
	}
	fmt.Fprintln(w, strings.Repeat("─", 58))
	switch OverallStatus(layers) {
	case StatusPass:
		fmt.Fprintln(w, cc("✓ "+summary+" — cluster healthy", bold+green))
	case StatusWarn:
		fmt.Fprintln(w, cc("⚠ "+summary+" — cluster degraded", bold+yellow))
	default:
		fmt.Fprintln(w, cc("✗ "+summary+" — cluster unhealthy", bold+red))
	}
}

// PrintJSON emits one JSON object per check result plus a summary object.
func PrintJSON(w io.Writer, layers []LayerResult) error {
	type jsonCheck struct {
		Timestamp string `json:"ts"`
		Phase     string `json:"phase"`
		Layer     int    `json:"layer"`
		Group     string `json:"group"`
		Name      string `json:"name"`
		Status    string `json:"status"`
		Message   string `json:"message,omitempty"`
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	enc := json.NewEncoder(w)
	for _, layer := range layers {
		for _, cr := range layer.Checks {
			if err := enc.Encode(jsonCheck{
				Timestamp: ts,
				Phase:     "cluster-status",
				Layer:     layer.Layer,
				Group:     layer.Group,
				Name:      cr.Check.Name,
				Status:    cr.Result.Status.String(),
				Message:   cr.Result.Message,
			}); err != nil {
				return err
			}
		}
	}
	// Summary event
	overall := OverallStatus(layers)
	var total, failing, warning int
	for _, l := range layers {
		for _, cr := range l.Checks {
			total++
			switch cr.Result.Status {
			case StatusFail, StatusUnknown:
				failing++
			case StatusWarn:
				warning++
			}
		}
	}
	return enc.Encode(jsonCheck{
		Timestamp: ts,
		Phase:     "cluster-status",
		Name:      "summary",
		Status:    overall.String(),
		Message:   fmt.Sprintf("%d/%d passing, %d failing, %d warning(s)", total-failing-warning, total, failing, warning),
	})
}
