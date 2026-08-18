package truenas

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Reference is one live PersistentVolume, reduced to the claim state that
// matters and to every string in it that can name an object on the NAS.
type Reference struct {
	PVName  string
	PVPhase string
	// ClaimedBy is namespace/name of the PersistentVolumeClaim whose
	// spec.volumeName points at this PV. It is resolved from the claim, never
	// from the PV's own spec.claimRef: claimRef survives the claim it names, so
	// a Released PV still carries the reference that created it.
	ClaimedBy string
	// Tokens are the identifiers this PV asserts about the NAS: the CSI volume
	// handle, every volume attribute value, and the in-tree NFS/iSCSI fields a
	// statically provisioned PV would use instead.
	Tokens []string
}

// Released reports whether the PV is in a phase that permits reclaiming its
// backing storage. Any other phase — Bound, Available, Pending — means
// something may still bind to it.
func (r Reference) Released() bool {
	return r.PVPhase == string(corev1.VolumeReleased) || r.PVPhase == string(corev1.VolumeFailed)
}

// ResolveReferences reads every PersistentVolume in the cluster, not only those
// provisioned by the two democratic-csi drivers. A statically written PV can
// point at the same target or export with a handle of its own making, and it is
// just as much a reason not to delete the storage underneath it.
func ResolveReferences(ctx context.Context, kube kubernetes.Interface) ([]Reference, error) {
	claims := map[string]string{}
	pvcs, err := kube.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list persistentvolumeclaims: %w", err)
	}
	for _, pvc := range pvcs.Items {
		if pvc.Spec.VolumeName == "" {
			continue
		}
		claims[pvc.Spec.VolumeName] = pvc.Namespace + "/" + pvc.Name
	}

	pvs, err := kube.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list persistentvolumes: %w", err)
	}
	out := make([]Reference, 0, len(pvs.Items))
	for i := range pvs.Items {
		pv := &pvs.Items[i]
		out = append(out, Reference{
			PVName:    pv.Name,
			PVPhase:   string(pv.Status.Phase),
			ClaimedBy: claims[pv.Name],
			Tokens:    referenceTokens(pv),
		})
	}
	return out, nil
}

func referenceTokens(pv *corev1.PersistentVolume) []string {
	var tokens []string
	add := func(v string) {
		if v = strings.TrimSpace(v); v != "" {
			tokens = append(tokens, v)
		}
	}
	if csi := pv.Spec.CSI; csi != nil {
		add(csi.VolumeHandle)
		for _, v := range csi.VolumeAttributes {
			add(v)
		}
	}
	if nfs := pv.Spec.NFS; nfs != nil {
		add(nfs.Path)
	}
	if iscsi := pv.Spec.ISCSI; iscsi != nil {
		add(iscsi.IQN)
	}
	return tokens
}

// namesObject reports whether this PV asserts the given NAS identifier.
//
// Matching is by equality, with one exception: an iSCSI IQN carries the target
// name after a colon, so a target is matched by that suffix too. Substring
// matching is deliberately not used anywhere else — a PV's attributes also
// carry the NAS address and the pool name, which would otherwise match every
// object on the box and make everything look live.
func (r Reference) namesObject(identifier string) bool {
	if identifier == "" {
		return false
	}
	for _, t := range r.Tokens {
		if t == identifier {
			return true
		}
	}
	return false
}

// namesTarget matches an iSCSI target by bare name or as the suffix of an IQN.
func (r Reference) namesTarget(target string) bool {
	if target == "" {
		return false
	}
	for _, t := range r.Tokens {
		if t == target || strings.HasSuffix(t, ":"+target) {
			return true
		}
	}
	return false
}
