package display

import (
	"fmt"
	"os"
	"time"
)

// PhaseResult holds the outcome of a single bootstrap phase.
type PhaseResult struct {
	Name    string
	Status  string
	Message string
}

// SummaryData holds all information needed to generate a SUMMARY.txt.
type SummaryData struct {
	StartTime    time.Time
	Duration     time.Duration
	Status       string
	Command      string
	RunDir       string
	ExitError    error
	PhaseResults []PhaseResult
}

// WriteSummary writes a SUMMARY.txt file to path.
func WriteSummary(path string, data *SummaryData) error {
	errMsg := "none"
	if data.ExitError != nil {
		errMsg = data.ExitError.Error()
	}

	content := fmt.Sprintf("platformctl run summary\n========================\nCommand:   %s\nStarted:   %s\nDuration:  %s\nStatus:    %s\nError:     %s\n\nPhases run:    %d\nPhases failed: %d\n",
		data.Command,
		data.StartTime.UTC().Format("2006-01-02 15:04:05 UTC"),
		data.Duration.Round(time.Second),
		data.Status,
		errMsg,
		len(data.PhaseResults),
		countFailed(data.PhaseResults),
	)

	if len(data.PhaseResults) > 0 {
		content += "\n"
		for i, r := range data.PhaseResults {
			marker := MarkerCheck
			if r.Status == "failed" || r.Status == "broken" {
				marker = MarkerCross
			}
			line := fmt.Sprintf("  %s  Phase %d  %s", marker, i+1, r.Name)
			if r.Message != "" && (r.Status == "failed" || r.Status == "broken") {
				line += " — " + r.Message
			}
			content += line + "\n"
		}
	}

	content += fmt.Sprintf("\nLog dir: %s\n  console.log    — colored terminal output\n  structured.log — JSON events (one per line)\n  audit.log      — commands and flags invoked\n", data.RunDir)

	return os.WriteFile(path, []byte(content), 0644)
}

func countFailed(phases []PhaseResult) int {
	n := 0
	for _, p := range phases {
		if p.Status == "failed" || p.Status == "broken" {
			n++
		}
	}
	return n
}
