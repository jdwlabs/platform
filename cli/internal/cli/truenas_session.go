package cli

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/client-go/kubernetes"

	"github.com/jdwlabs/platform/internal/truenas"
)

// testTrueNASDialer replaces the middleware connection in tests. The
// destructive path is exercised against a fake middleware rather than the NAS,
// so this seam exists for the same reason testKubeClient does.
var testTrueNASDialer func(ctx context.Context, cfg truenas.DriverConfig, tls truenas.TLSOptions) (truenas.Caller, error)

// truenasSession pairs one driver config with the connection its objects are
// read and deleted over.
type truenasSession struct {
	cfg  truenas.DriverConfig
	call truenas.Caller
	kube kubernetes.Interface
	// owned is false for the second class on a shared endpoint, so Close only
	// shuts a connection down once.
	owned bool
}

type truenasSessions []*truenasSession

// openTrueNASSessions reads both driver configs and connects to the NAS each
// one names. Both classes are configured against the same host today, so a
// second class reuses the first connection rather than authenticating twice.
func openTrueNASSessions(ctx context.Context, shared *truenasGlobals) (truenasSessions, error) {
	classes, err := selectedClasses(shared.StorageClass)
	if err != nil {
		return nil, err
	}
	kube, err := volumeKubeClient()
	if err != nil {
		return nil, err
	}
	cfgs, err := truenas.LoadDriverConfigs(ctx, kube, classes, shared.UseCSIKey)
	if err != nil {
		return nil, err
	}

	tlsOpts := truenas.TLSOptions{CAFile: shared.CAFile, SkipVerify: shared.SkipVerify}
	byEndpoint := map[string]truenas.Caller{}
	var out truenasSessions
	for _, cfg := range cfgs {
		endpoint := cfg.Endpoint()
		call, reused := byEndpoint[endpoint]
		if !reused {
			call, err = dialTrueNAS(ctx, cfg, tlsOpts)
			if err != nil {
				out.Close()
				return nil, err
			}
			byEndpoint[endpoint] = call
		}
		out = append(out, &truenasSession{cfg: cfg, call: call, kube: kube, owned: !reused})
	}
	return out, nil
}

func dialTrueNAS(ctx context.Context, cfg truenas.DriverConfig, tlsOpts truenas.TLSOptions) (truenas.Caller, error) {
	if testTrueNASDialer != nil {
		return testTrueNASDialer(ctx, cfg, tlsOpts)
	}
	return truenas.Dial(ctx, cfg.Endpoint(), cfg.APIKey(), tlsOpts)
}

func (s truenasSessions) Close() {
	for _, sess := range s {
		if sess != nil && sess.owned && sess.call != nil {
			_ = sess.call.Close()
		}
	}
}

// Classify reads every session's inventory and cross-references it against the
// cluster's PersistentVolumes, which are read once and shared: a claim is a
// property of the cluster, not of one storage class.
//
// The warnings it returns are what changes the meaning of the verdicts without
// failing the command: a read that degraded, and a check the driver config
// cannot support. Either one is reported at the top of the output rather than
// left implicit in the reason on rows an operator may have filtered away.
func (s truenasSessions) Classify(ctx context.Context) ([]truenas.Candidate, []string, error) {
	if len(s) == 0 {
		return nil, nil, nil
	}
	refs, err := truenas.ResolveReferences(ctx, s[0].kube)
	if err != nil {
		return nil, nil, err
	}
	var out []truenas.Candidate
	var warnings []string
	for _, sess := range s {
		inv, err := truenas.ReadInventory(ctx, sess.call, sess.cfg)
		if err != nil {
			return nil, nil, err
		}
		if !inv.SessionsKnown {
			warnings = append(warnings, fmt.Sprintf(
				"%s: the iSCSI session list could not be read, so no zvol can be proved idle: %s",
				sess.cfg.StorageClass, inv.SessionsError))
		}
		// A skipped stray-target scan looks identical to a clean NAS, and it is
		// the scan a resumed run depends on: a plan that failed at the target
		// step leaves a target mapped to no extent and nothing else to find it.
		if sess.cfg.StorageClass == truenas.ClassISCSI && !sess.cfg.DetectsStrayTargets() {
			warnings = append(warnings, fmt.Sprintf(
				"%s: iscsi.namePrefix/nameSuffix are unset in the driver config, so a target left behind "+
					"by a partial delete cannot be told from an unrelated one and is not reported",
				sess.cfg.StorageClass))
		}
		out = append(out, truenas.Classify(sess.cfg, inv, refs)...)
	}
	sortTrueNASCandidates(out)
	return out, warnings, nil
}

// Reclaim executes each candidate's plan in order and returns the candidates
// whose plan completed, plus the name of the one that stopped the run. The
// first failure stops it: the plans are ordered dependency chains, so
// continuing past a failed step would leave rows behind that the next command
// has no way to attribute.
//
// The failed name is returned rather than folded into the error because that
// candidate is in neither table — its plan is partly executed — and it is the
// one an operator has to look at before resuming.
func (s truenasSessions) Reclaim(ctx context.Context, selected []truenas.Candidate) (
	deleted []truenas.Candidate, failed string, err error) {
	// One Reclaimer per class, not per candidate: it caches the run's single
	// re-read of the cluster's PersistentVolumes.
	reclaimers := map[string]*truenas.Reclaimer{}
	for _, sess := range s {
		reclaimers[sess.cfg.StorageClass] = truenas.NewReclaimer(sess.call, sess.kube)
	}
	for _, c := range selected {
		r, ok := reclaimers[c.StorageClass]
		if !ok {
			return deleted, c.Name, fmt.Errorf("no open connection for storage class %s", c.StorageClass)
		}
		if err := r.Run(ctx, c); err != nil {
			return deleted, c.Name, err
		}
		deleted = append(deleted, c)
	}
	return deleted, "", nil
}

func selectedClasses(storageClass string) ([]string, error) {
	if storageClass == "all" {
		return truenas.Classes(), nil
	}
	if containsString(truenas.Classes(), storageClass) {
		return []string{storageClass}, nil
	}
	return nil, fmt.Errorf("unknown --storage-class %s; valid: all, %s",
		storageClass, strings.Join(truenas.Classes(), ", "))
}
