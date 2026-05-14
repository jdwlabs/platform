package helm

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
)

// InstallOpts holds all options for helm upgrade --install.
type InstallOpts struct {
	ValuesFiles []string          // --values flags (paths to YAML files)
	SetValues   map[string]string // --set key=value pairs
	Namespace   string
}

// Runner abstracts helm upgrade --install. Production code uses ExecRunner;
// tests inject FakeRunner to avoid requiring a real helm binary or cluster.
type Runner interface {
	UpgradeInstall(ctx context.Context, release, chartRef string, opts InstallOpts) error
}

// ExecRunner calls the helm binary found on $PATH.
type ExecRunner struct{}

func (ExecRunner) UpgradeInstall(ctx context.Context, release, chartRef string, opts InstallOpts) error {
	args := []string{"upgrade", "--install", release, chartRef, "-n", opts.Namespace, "--create-namespace"}
	for _, f := range opts.ValuesFiles {
		args = append(args, "--values", f)
	}
	for k, v := range opts.SetValues {
		args = append(args, "--set", k+"="+v)
	}
	out, err := exec.CommandContext(ctx, "helm", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("helm %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return nil
}

// FakeRunner records calls for tests. Never fails unless Err is set.
type FakeRunner struct {
	Calls []string
	Err   error
}

func (f *FakeRunner) UpgradeInstall(_ context.Context, release, chartRef string, _ InstallOpts) error {
	f.Calls = append(f.Calls, release+"/"+chartRef)
	return f.Err
}

// LoadChart loads a Helm chart from a directory or .tgz path.
func LoadChart(path string) (*chart.Chart, error) {
	c, err := loader.Load(path)
	if err != nil {
		return nil, fmt.Errorf("load chart %s: %w", path, err)
	}
	return c, nil
}
