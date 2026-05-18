package cluster

import (
	"context"
	"fmt"
)

// Status is the health outcome of a single Check.
type Status int

const (
	StatusPass Status = iota
	StatusWarn
	StatusFail
	StatusUnknown
	StatusSkipped
)

func (s Status) String() string {
	switch s {
	case StatusPass:
		return "pass"
	case StatusWarn:
		return "warn"
	case StatusFail:
		return "fail"
	case StatusSkipped:
		return "skipped"
	default:
		return "unknown"
	}
}

func (s Status) Glyph() string {
	switch s {
	case StatusPass:
		return "✓"
	case StatusWarn:
		return "⚠"
	case StatusFail:
		return "✗"
	case StatusSkipped:
		return "–"
	default:
		return "?"
	}
}

// Check is one atomic health check.
type Check struct {
	Layer int
	Group string // layer display name, e.g. "Operators"
	Name  string // short label, e.g. "argocd-server"
	Run   func(ctx context.Context) Result
}

// Result is the outcome of Check.Run.
type Result struct {
	Status  Status
	Message string
}

func Pass(msg string) Result    { return Result{StatusPass, msg} }
func Fail(msg string) Result    { return Result{StatusFail, msg} }
func Warn(msg string) Result    { return Result{StatusWarn, msg} }
func Skip(msg string) Result    { return Result{StatusSkipped, msg} }
func Unknown(msg string) Result { return Result{StatusUnknown, msg} }

func Failf(format string, args ...any) Result { return Fail(fmt.Sprintf(format, args...)) }
func Warnf(format string, args ...any) Result { return Warn(fmt.Sprintf(format, args...)) }
func Passf(format string, args ...any) Result { return Pass(fmt.Sprintf(format, args...)) }

// CheckResult pairs a Check with its run Result.
type CheckResult struct {
	Check  Check
	Result Result
}

// LayerResult is the aggregate result for one layer.
type LayerResult struct {
	Layer  int
	Group  string
	Checks []CheckResult
}

// OverallStatus returns the worst status across all checks in this layer.
func (l LayerResult) OverallStatus() Status {
	worst := StatusPass
	for _, cr := range l.Checks {
		if cr.Result.Status > worst {
			worst = cr.Result.Status
		}
	}
	return worst
}
