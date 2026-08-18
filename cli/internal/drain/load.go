package drain

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	resourcehelper "k8s.io/component-helpers/resource"
)

// MirrorPodAnnotation marks a pod the kubelet owns rather than the API server.
const MirrorPodAnnotation = corev1.MirrorPodAnnotationKey

// ControlPlaneLabel identifies a control-plane node. Reported so an operator can
// tell a control-plane verdict from a worker one; it does not affect placement,
// which is decided by taints and affinity like any other node.
const ControlPlaneLabel = "node-role.kubernetes.io/control-plane"

// Load reads the live cluster state a simulation needs. It is read-only: five
// list calls and nothing else.
func Load(ctx context.Context, kc kubernetes.Interface) (Cluster, error) {
	var c Cluster

	nodeList, err := kc.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return c, fmt.Errorf("list nodes: %w", err)
	}
	for _, n := range nodeList.Items {
		c.Nodes = append(c.Nodes, convertNode(n))
	}

	podList, err := kc.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return c, fmt.Errorf("list pods: %w", err)
	}

	pinning, err := loadVolumePinning(ctx, kc)
	if err != nil {
		return c, err
	}

	unmodelled := map[string]bool{}
	for i := range podList.Items {
		pod := podList.Items[i]
		if pod.Spec.NodeName == "" {
			continue
		}
		p := convertPod(&pod, pinning)
		for _, u := range UsesUnmodelledConstraint(&pod.Spec) {
			unmodelled[p.Workload+": "+u] = true
		}
		c.Pods = append(c.Pods, p)
	}

	pdbList, err := kc.PolicyV1().PodDisruptionBudgets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return c, fmt.Errorf("list poddisruptionbudgets: %w", err)
	}
	MatchPDBs(c.Pods, pdbList.Items)

	for u := range unmodelled {
		c.UnmodelledConstraints = append(c.UnmodelledConstraints, u)
	}
	sort.Strings(c.UnmodelledConstraints)
	sort.Slice(c.Nodes, func(i, j int) bool { return c.Nodes[i].Name < c.Nodes[j].Name })
	return c, nil
}

func convertNode(n corev1.Node) Node {
	node := Node{
		Name:              n.Name,
		Labels:            n.Labels,
		Taints:            n.Spec.Taints,
		Cordoned:          n.Spec.Unschedulable,
		NotReady:          !nodeReady(n),
		AllocatableMemory: n.Status.Allocatable.Memory().Value(),
		AllocatableCPU:    n.Status.Allocatable.Cpu().MilliValue(),
		AllocatablePods:   n.Status.Allocatable.Pods().Value(),
	}
	_, node.ControlPlane = n.Labels[ControlPlaneLabel]
	return node
}

func nodeReady(n corev1.Node) bool {
	for _, cond := range n.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

func convertPod(pod *corev1.Pod, pinning map[string][]*corev1.NodeSelector) Pod {
	spec := pod.Spec
	requests := resourcehelper.PodRequests(pod, resourcehelper.PodResourcesOptions{})

	p := Pod{
		Namespace: pod.Namespace,
		Name:      pod.Name,
		NodeName:  pod.Spec.NodeName,
		Labels:    pod.Labels,
		Memory:    requests.Memory().Value(),
		CPU:       requests.Cpu().MilliValue(),
		Class:     classify(pod),
		Workload:  workloadName(pod),
		Spec:      &spec,
	}
	p.Unmanaged = p.Class == ClassMovable && len(pod.OwnerReferences) == 0
	p.VolumeNodeSelectors = volumeSelectors(pod, pinning)
	return p
}

func classify(pod *corev1.Pod) PodClass {
	switch pod.Status.Phase {
	case corev1.PodSucceeded, corev1.PodFailed:
		return ClassFinished
	}
	if pod.DeletionTimestamp != nil {
		return ClassTerminating
	}
	if _, ok := pod.Annotations[MirrorPodAnnotation]; ok {
		return ClassMirror
	}
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "DaemonSet" {
			return ClassDaemonSet
		}
	}
	return ClassMovable
}

// workloadName names the controller a pod belongs to, so several replicas of
// one thing report as one thing.
func workloadName(pod *corev1.Pod) string {
	for _, ref := range pod.OwnerReferences {
		if ref.Controller != nil && *ref.Controller {
			return pod.Namespace + "/" + ref.Name
		}
	}
	return pod.Namespace + "/" + pod.Name
}

// loadVolumePinning maps namespace/pvc-name to the node affinity of the
// PersistentVolume behind it. A node-local volume — local-path-provisioner, a
// hostPath PV — pins its pod to one node outright, which is the difference
// between a drain that is tight and one that is impossible.
func loadVolumePinning(ctx context.Context, kc kubernetes.Interface) (map[string][]*corev1.NodeSelector, error) {
	pvcList, err := kc.CoreV1().PersistentVolumeClaims(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list persistentvolumeclaims: %w", err)
	}
	pvList, err := kc.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list persistentvolumes: %w", err)
	}

	affinityByPV := map[string]*corev1.NodeSelector{}
	for _, pv := range pvList.Items {
		if pv.Spec.NodeAffinity != nil && pv.Spec.NodeAffinity.Required != nil {
			affinityByPV[pv.Name] = pv.Spec.NodeAffinity.Required
		}
	}

	out := map[string][]*corev1.NodeSelector{}
	for _, pvc := range pvcList.Items {
		if sel, ok := affinityByPV[pvc.Spec.VolumeName]; ok {
			key := pvc.Namespace + "/" + pvc.Name
			out[key] = append(out[key], sel)
		}
	}
	return out, nil
}

func volumeSelectors(pod *corev1.Pod, pinning map[string][]*corev1.NodeSelector) []*corev1.NodeSelector {
	var out []*corev1.NodeSelector
	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim == nil {
			continue
		}
		out = append(out, pinning[pod.Namespace+"/"+v.PersistentVolumeClaim.ClaimName]...)
	}
	return out
}
