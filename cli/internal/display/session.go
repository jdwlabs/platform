package display

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Config holds options for creating a RunSession.
type Config struct {
	LogDir  string // default: $HOME/.platformctl/runs
	Command string // "bootstrap", "heal", "tenants"
	NoColor bool
}

// RunSession manages a single platformctl run's log files and lifecycle.
type RunSession struct {
	RunDir      string
	StartTime   time.Time
	Logger      *zap.Logger
	AuditLog    *AuditLogger
	Console     io.Writer // tees stderr + console.log
	ConsoleFile io.Writer // console.log only
	NoColor     bool
	Command     string
	closers     []io.Closer
	runsLogDir  string

	PhasesRun    int
	PhasesFailed int
	phaseResults []PhaseResult
}

// NewRunSession creates a timestamped run directory, opens log files, and builds
// a tee'd zap.Logger writing to stderr+console.log (kvEncoder) and structured.log (JSON).
func NewRunSession(cfg Config) (*RunSession, error) {
	logDir := cfg.LogDir
	if logDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home dir: %w", err)
		}
		logDir = filepath.Join(home, ".platformctl", "runs")
	}

	now := time.Now()
	dateDir := now.Format("2006-01-02")
	runName := "run-" + now.Format("20060102_150405")
	runDir := filepath.Join(logDir, dateDir, runName)

	if err := os.MkdirAll(runDir, 0755); err != nil {
		return nil, fmt.Errorf("create run directory %s: %w", runDir, err)
	}

	consoleFile, err := os.Create(filepath.Join(runDir, "console.log"))
	if err != nil {
		return nil, fmt.Errorf("create console.log: %w", err)
	}

	structuredFile, err := os.Create(filepath.Join(runDir, "structured.log"))
	if err != nil {
		_ = consoleFile.Close()
		return nil, fmt.Errorf("create structured.log: %w", err)
	}

	auditFile, err := os.Create(filepath.Join(runDir, "audit.log"))
	if err != nil {
		_ = consoleFile.Close()
		_ = structuredFile.Close()
		return nil, fmt.Errorf("create audit.log: %w", err)
	}

	teeCore := buildTeeCore(cfg.NoColor, consoleFile, structuredFile)
	logger := zap.New(teeCore, zap.AddStacktrace(zap.FatalLevel))

	sess := &RunSession{
		RunDir:      runDir,
		StartTime:   now,
		Logger:      logger,
		AuditLog:    NewAuditLogger(auditFile),
		Console:     io.MultiWriter(os.Stderr, consoleFile),
		ConsoleFile: consoleFile,
		NoColor:     cfg.NoColor,
		Command:     cfg.Command,
		closers:     []io.Closer{consoleFile, structuredFile, auditFile},
		runsLogDir:  logDir,
	}

	sess.registerRun()
	sess.updateLatest()

	return sess, nil
}

// buildTeeCore creates a core fanning out to stderr+console.log (kvEncoder) and structured.log (JSON).
func buildTeeCore(noColor bool, consoleFile, structuredFile io.Writer) zapcore.Core {
	level := zapcore.InfoLevel
	kvEnc := &kvEncoder{
		inner:   zapcore.NewConsoleEncoder(newConsoleEncoderConfig(noColor)),
		noColor: noColor,
	}

	jsonCfg := zap.NewProductionEncoderConfig()
	jsonCfg.TimeKey = "ts"
	jsonCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	jsonEnc := zapcore.NewJSONEncoder(jsonCfg)

	enabler := zap.LevelEnablerFunc(func(l zapcore.Level) bool { return l >= level })

	return zapcore.NewTee(
		zapcore.NewCore(kvEnc, zapcore.Lock(os.Stderr), enabler),
		zapcore.NewCore(kvEnc.Clone(), zapcore.AddSync(consoleFile), enabler),
		zapcore.NewCore(jsonEnc, zapcore.AddSync(structuredFile), enabler),
	)
}

// RecordPhase records the outcome of a phase for the end-of-run summary.
func (s *RunSession) RecordPhase(name, status, message string) {
	s.PhasesRun++
	if status == "failed" || status == "broken" {
		s.PhasesFailed++
	}
	s.phaseResults = append(s.phaseResults, PhaseResult{
		Name:    name,
		Status:  status,
		Message: message,
	})
}

// PrintSummaryBox writes the end-of-run box to Console.
func (s *RunSession) PrintSummaryBox(exitErr error) {
	box := NewBox(s.Console, s.NoColor)
	box.Header("BOOTSTRAP SUMMARY")

	overallStatus := "SUCCESS"
	if exitErr != nil {
		overallStatus = "FAILED"
	}

	duration := time.Since(s.StartTime).Round(time.Second)
	box.Row("Status", overallStatus)
	box.Row("Duration", duration.String())
	if s.PhasesRun > 0 {
		box.Row("Phases", fmt.Sprintf("%d/%d", s.PhasesRun-s.PhasesFailed, s.PhasesRun))
	}

	if len(s.phaseResults) > 0 {
		box.Divider()
		for i, r := range s.phaseResults {
			marker := MarkerCheck
			if r.Status == "failed" || r.Status == "broken" {
				marker = MarkerCross
			}
			text := fmt.Sprintf("Phase %d  %s", i+1, r.Name)
			if r.Message != "" && marker == MarkerCross {
				text += " — " + r.Message
			}
			box.Item(marker, text)
		}
	}

	box.Footer()
	_, _ = fmt.Fprintf(s.Console, "Run log: %s\n", s.RunDir)
}

// Close finalizes the session: writes SUMMARY.txt, updates runs.log, flushes zap, closes files.
func (s *RunSession) Close(exitErr error) {
	duration := time.Since(s.StartTime)
	status := "success"
	if exitErr != nil {
		status = "failed"
	}

	summaryData := &SummaryData{
		StartTime:    s.StartTime,
		Duration:     duration,
		Status:       strings.ToUpper(status),
		Command:      s.Command,
		RunDir:       s.RunDir,
		ExitError:    exitErr,
		PhaseResults: s.phaseResults,
	}
	_ = WriteSummary(filepath.Join(s.RunDir, "SUMMARY.txt"), summaryData)

	s.updateRunsLogStatus(status)

	_ = s.Logger.Sync()

	for _, c := range s.closers {
		_ = c.Close()
	}
}

func (s *RunSession) registerRun() {
	runsLogPath := filepath.Join(s.runsLogDir, "runs.log")
	_ = os.MkdirAll(filepath.Dir(runsLogPath), 0755)
	entry := fmt.Sprintf("%s|%s|%s|pending\n",
		s.StartTime.Format("2006-01-02 15:04:05"),
		s.Command,
		s.RunDir,
	)
	f, err := os.OpenFile(runsLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.WriteString(entry)
}

func (s *RunSession) updateLatest() {
	latestPath := filepath.Join(s.runsLogDir, "latest.txt")
	_ = os.MkdirAll(filepath.Dir(latestPath), 0755)
	_ = os.WriteFile(latestPath, []byte(s.RunDir+"\n"), 0644)
}

func (s *RunSession) updateRunsLogStatus(status string) {
	runsLogPath := filepath.Join(s.runsLogDir, "runs.log")
	data, err := os.ReadFile(runsLogPath)
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.Contains(line, s.RunDir) && strings.HasSuffix(line, "pending") {
			lines[i] = strings.TrimSuffix(line, "pending") + status
			break
		}
	}
	_ = os.WriteFile(runsLogPath, []byte(strings.Join(lines, "\n")), 0644)
}
