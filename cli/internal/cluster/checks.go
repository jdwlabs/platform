package cluster

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/jdwlabs/platform/internal/rclone"
)

var (
	gvrGatewayClass        = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gatewayclasses"}
	gvrGateway             = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}
	gvrHTTPRoute           = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	gvrClusterSecretStore  = schema.GroupVersionResource{Group: "external-secrets.io", Version: "v1", Resource: "clustersecretstores"}
	gvrExternalSecret      = schema.GroupVersionResource{Group: "external-secrets.io", Version: "v1", Resource: "externalsecrets"}
	gvrCertificate         = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	gvrApplication         = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	gvrLonghornEngineImage = schema.GroupVersionResource{Group: "longhorn.io", Version: "v1beta2", Resource: "engineimages"}
	gvrLonghornSetting     = schema.GroupVersionResource{Group: "longhorn.io", Version: "v1beta2", Resource: "settings"}
)

// AllChecks returns the full ordered health check list for the platform cluster.
func AllChecks(kube kubernetes.Interface, dyn dynamic.Interface) []Check {
	var checks []Check
	checks = append(checks, operatorChecks(kube, dyn)...)
	checks = append(checks, vaultChecks(kube, dyn)...)
	checks = append(checks, secretChecks(kube, dyn)...)
	checks = append(checks, tlsChecks(kube, dyn)...)
	checks = append(checks, argocdChecks(kube, dyn)...)
	checks = append(checks, policyChecks(kube)...)
	return checks
}

// --- Layer 1: Operators ---

func operatorChecks(kube kubernetes.Interface, dyn dynamic.Interface) []Check {
	return []Check{
		{Layer: 1, Group: "Operators", Name: "argocd-server", Run: func(ctx context.Context) Result {
			return checkDeploymentByWorkloadName(ctx, kube, "argocd", "argocd-server")
		}},
		{Layer: 1, Group: "Operators", Name: "external-secrets", Run: func(ctx context.Context) Result {
			return checkDeploymentByWorkloadName(ctx, kube, "external-secrets", "external-secrets")
		}},
		{Layer: 1, Group: "Operators", Name: "cert-manager", Run: func(ctx context.Context) Result {
			return checkDeploymentByWorkloadName(ctx, kube, "cert-manager", "cert-manager")
		}},
		{Layer: 1, Group: "Operators", Name: "nginx-gateway-fabric", Run: func(ctx context.Context) Result {
			return checkGatewayClassAccepted(ctx, dyn, "nginx")
		}},
		{Layer: 1, Group: "Operators", Name: "longhorn", Run: func(ctx context.Context) Result {
			return checkStorageClassExists(ctx, kube, "longhorn")
		}},
		{Layer: 1, Group: "Operators", Name: "longhorn-engine-skew", Run: func(ctx context.Context) Result {
			return checkLonghornEngineVersionSkew(ctx, dyn)
		}},
		{Layer: 1, Group: "Operators", Name: "vault-pod", Run: func(ctx context.Context) Result {
			return checkVaultPodReady(ctx, kube)
		}},
		{Layer: 1, Group: "Operators", Name: "statefulset-revisions", Run: func(ctx context.Context) Result {
			return checkStatefulSetRevisions(ctx, kube)
		}},
	}
}

// --- Layer 2: Vault init state ---

func vaultChecks(kube kubernetes.Interface, dyn dynamic.Interface) []Check {
	return []Check{
		{Layer: 2, Group: "Vault", Name: "vault-init-secret", Run: func(ctx context.Context) Result {
			return checkSecretExists(ctx, kube, "vault", "vault-init")
		}},
		{Layer: 2, Group: "Vault", Name: "vault-token-secret", Run: func(ctx context.Context) Result {
			return checkSecretExists(ctx, kube, "external-secrets", "vault-token")
		}},
		{Layer: 2, Group: "Vault", Name: "cluster-secret-store", Run: func(ctx context.Context) Result {
			return checkClusterSecretStoreReady(ctx, dyn, "vault")
		}},
	}
}

// --- Layer 3: ExternalSecrets ---

func secretChecks(kube kubernetes.Interface, dyn dynamic.Interface) []Check {
	type target struct{ name, ns string }
	targets := []target{
		{"porkbun", "cert-manager"},
		{"longhorn", "longhorn-system"},
		{"grafana-admin-credentials", "monitoring"},
		{"alertmanager-config", "monitoring"},
		{"rclone-gdrive", "database"},
	}
	checks := make([]Check, len(targets))
	for i, t := range targets {
		t := t
		checks[i] = Check{
			Layer: 3,
			Group: "Secrets",
			Name:  t.name + "/" + t.ns,
			Run: func(ctx context.Context) Result {
				return checkExternalSecretSynced(ctx, dyn, t.ns, t.name)
			},
		}
	}
	checks = append(checks, Check{
		Layer: 3,
		Group: "Secrets",
		Name:  "rclone-gdrive-client-id",
		Run: func(ctx context.Context) Result {
			return checkRcloneOwnClientID(ctx, kube, "database", "rclone-gdrive")
		},
	})
	return checks
}

// checkRcloneOwnClientID reports whether the Drive remote backing the postgres
// backups authenticates as its own OAuth application. Google is retiring the
// credentials rclone bundles, and the only warning before that is a NOTICE in
// backup job logs that are garbage-collected within a day — so the state is
// asserted here instead, where it survives.
//
// Warn rather than fail while the remote is still on the shared client: uploads
// work until the retirement lands, and a red check would say otherwise.
func checkRcloneOwnClientID(ctx context.Context, kube kubernetes.Interface, ns, name string) Result {
	secret, err := kube.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return Fail("secret not found")
	}
	if err != nil {
		return Failf("get error: %v", err)
	}
	conf, ok := secret.Data["rclone.conf"]
	if !ok {
		return Fail("secret has no rclone.conf key")
	}
	switch rclone.InspectRemote(string(conf), "gdrive") {
	case rclone.OwnClient:
		return Pass("dedicated OAuth client_id")
	case rclone.PartialClient:
		return Fail("client_id and client_secret must both be set; the remote will fail at its next token refresh")
	default:
		return Warn("using rclone's shared client_id, which Google is retiring during 2026")
	}
}

// --- Layer 4: TLS / Gateway routing ---

func tlsChecks(kube kubernetes.Interface, dyn dynamic.Interface) []Check {
	return []Check{
		{Layer: 4, Group: "TLS/Routing", Name: "cert/wildcard-jdwlabs", Run: func(ctx context.Context) Result {
			return checkCertificateReady(ctx, dyn, "nginx-gateway", "wildcard-jdwlabs")
		}},
		{Layer: 4, Group: "TLS/Routing", Name: "gateway/platform-gateway", Run: func(ctx context.Context) Result {
			return checkGatewayProgrammed(ctx, dyn, "nginx-gateway", "platform-gateway")
		}},
		{Layer: 4, Group: "TLS/Routing", Name: "httproute/vault", Run: func(ctx context.Context) Result {
			return checkHTTPRouteAccepted(ctx, dyn, "vault", "vault")
		}},
		{Layer: 4, Group: "TLS/Routing", Name: "httproute/argocd", Run: func(ctx context.Context) Result {
			return checkHTTPRouteAccepted(ctx, dyn, "argocd", "argocd")
		}},
		{Layer: 4, Group: "TLS/Routing", Name: "httproute/http-redirect", Run: func(ctx context.Context) Result {
			return checkHTTPRouteAccepted(ctx, dyn, "nginx-gateway", "http-to-https-redirect")
		}},
	}
}

// --- Layer 5: ArgoCD applications ---

func argocdChecks(kube kubernetes.Interface, dyn dynamic.Interface) []Check {
	return []Check{
		{Layer: 5, Group: "ArgoCD", Name: "applications", Run: func(ctx context.Context) Result {
			return checkAllApplicationsHealthy(ctx, dyn)
		}},
		{Layer: 5, Group: "ArgoCD", Name: "image-drift", Run: func(ctx context.Context) Result {
			return checkArgoWorkloadImageDrift(ctx, kube, dyn)
		}},
	}
}

// --- Layer 6: Policy adoption ---

func policyChecks(kube kubernetes.Interface) []Check {
	return []Check{
		{Layer: 6, Group: "Policy", Name: "limitrange-adoption", Run: func(ctx context.Context) Result {
			return checkLimitRangeAdoption(ctx, kube)
		}},
	}
}

// --- Check implementations ---

// checkDeploymentByWorkloadName finds the Deployment in ns with the
// app.kubernetes.io/name=<workloadName> label and reports its Available
// condition. This decouples the probe from Helm release naming, so charts
// installed with arbitrary release prefixes (e.g. "platform-cert-manager")
// still match.
func checkDeploymentByWorkloadName(ctx context.Context, kube kubernetes.Interface, ns, workloadName string) Result {
	list, err := kube.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=" + workloadName,
	})
	if k8serrors.IsNotFound(err) {
		return Failf("namespace %s not found", ns)
	}
	if err != nil {
		return Failf("list error: %v", err)
	}
	if len(list.Items) == 0 {
		return Failf("no Deployment with app.kubernetes.io/name=%s in ns %s", workloadName, ns)
	}
	d := list.Items[0]
	for _, cond := range d.Status.Conditions {
		if cond.Type == appsv1.DeploymentAvailable && cond.Status == corev1.ConditionTrue {
			return Passf("Available (%d/%d ready)", d.Status.ReadyReplicas, d.Status.Replicas)
		}
	}
	return Failf("not Available (%d/%d ready)", d.Status.ReadyReplicas, d.Status.Replicas)
}

func checkSecretExists(ctx context.Context, kube kubernetes.Interface, ns, name string) Result {
	_, err := kube.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return Fail("secret not found")
	}
	if err != nil {
		return Failf("get error: %v", err)
	}
	return Pass("present")
}

func checkStorageClassExists(ctx context.Context, kube kubernetes.Interface, name string) Result {
	_, err := kube.StorageV1().StorageClasses().Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return Fail("StorageClass not found")
	}
	if err != nil {
		return Failf("get error: %v", err)
	}
	return Pass("StorageClass present")
}

// checkLonghornEngineVersionSkew flags data-plane engine images running a
// minor version behind the Longhorn control plane.
//
// concurrentAutomaticEngineUpgradePerNodeLimit defaults to (and here is
// pinned to) 0, so a chart bump that advances current-longhorn-version never
// carries the data plane along with it — engines stay on whatever
// EngineImage they were attached to until someone runs a manual spec.image
// patch per volume. A one-minor manager-ahead-of-engine gap is supported;
// nothing catches it drifting to two minors before the next chart bump lands,
// which is unsupported and breaks volume attachment. Comparing EngineImages
// actually in use (status.refCount > 0) against the control-plane setting is
// that missing signal — the StorageClass-presence check above only proves
// Longhorn is installed, not that its data plane matches its control plane.
func checkLonghornEngineVersionSkew(ctx context.Context, dyn dynamic.Interface) Result {
	setting, err := dyn.Resource(gvrLonghornSetting).Namespace("longhorn-system").
		Get(ctx, "current-longhorn-version", metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return Warn("current-longhorn-version setting not found")
	}
	if err != nil {
		return Failf("get current-longhorn-version: %v", err)
	}
	controlPlane, _, _ := unstructured.NestedString(setting.Object, "value")
	cpMajor, cpMinor, ok := parseMajorMinor(controlPlane)
	if !ok {
		return Warnf("could not parse current-longhorn-version %q", controlPlane)
	}

	images, err := dyn.Resource(gvrLonghornEngineImage).Namespace("longhorn-system").List(ctx, metav1.ListOptions{})
	if err != nil {
		return Failf("list engineimages: %v", err)
	}

	var maxGap int
	var skewed []string
	var unparsed []string
	for _, img := range images.Items {
		refCount, _, _ := unstructured.NestedInt64(img.Object, "status", "refCount")
		if refCount == 0 {
			continue // not in use by any engine or replica
		}
		image, _, _ := unstructured.NestedString(img.Object, "spec", "image")
		major, minor, ok := parseMajorMinor(image)
		if !ok {
			unparsed = append(unparsed, img.GetName())
			continue
		}
		if major != cpMajor {
			continue // different major line — not a minor-skew comparison
		}
		gap := cpMinor - minor
		if gap <= 0 {
			continue
		}
		if gap > maxGap {
			maxGap = gap
		}
		skewed = append(skewed, fmt.Sprintf("%s v%d.%d (refCount=%d)", img.GetName(), major, minor, refCount))
	}

	if len(skewed) == 0 {
		if len(unparsed) > 0 {
			return Warnf("could not parse version for in-use engine image(s): %s", strings.Join(unparsed, ", "))
		}
		return Passf("all in-use engine images match control plane v%d.%d", cpMajor, cpMinor)
	}
	msg := fmt.Sprintf("control plane v%d.%d, %d minor(s) behind: %s",
		cpMajor, cpMinor, maxGap, strings.Join(skewed, ", "))
	if maxGap >= 2 {
		return Fail(msg)
	}
	return Warn(msg)
}

// parseMajorMinor extracts the major.minor pair from a "vX.Y.Z" version
// string, or an image reference ending in ":vX.Y.Z".
func parseMajorMinor(s string) (major, minor int, ok bool) {
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimPrefix(s, "v")
	parts := strings.SplitN(s, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, errMajor := strconv.Atoi(parts[0])
	minor, errMinor := strconv.Atoi(parts[1])
	if errMajor != nil || errMinor != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// checkVaultPodReady asserts Vault pods are Ready, not merely Running.
//
// Readiness is the seal signal. The chart's readiness probe runs `vault status`,
// whose exit code encodes seal state (0 unsealed, 2 sealed), so a sealed Vault
// is Running-but-not-Ready. Asserting on pod phase alone reports a sealed
// Vault — one serving no secrets to anything — as healthy. Reading the pod
// condition rather than calling /v1/sys/health keeps the check dependency-free:
// it needs no route to Vault and no token, only the API server already in use.
func checkVaultPodReady(ctx context.Context, kube kubernetes.Interface) Result {
	pods, err := kube.CoreV1().Pods("vault").List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=vault",
	})
	if k8serrors.IsNotFound(err) {
		return Fail("vault namespace not found")
	}
	if err != nil {
		return Failf("list pods: %v", err)
	}
	if len(pods.Items) == 0 {
		return Fail("no vault pods found")
	}

	var ready, notReady []string
	for _, pod := range pods.Items {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
				return Failf("pod %s: CrashLoopBackOff", pod.Name)
			}
		}
		if pod.Status.Phase != corev1.PodRunning {
			notReady = append(notReady, fmt.Sprintf("%s (phase=%s)", pod.Name, pod.Status.Phase))
			continue
		}
		if podIsReady(pod) {
			ready = append(ready, pod.Name)
		} else {
			notReady = append(notReady, fmt.Sprintf("%s (Running, not Ready — sealed or starting)", pod.Name))
		}
	}

	switch {
	case len(notReady) == 0:
		return Passf("%d/%d pods Ready (unsealed)", len(ready), len(pods.Items))
	case len(ready) == 0:
		return Failf("0/%d pods Ready: %s", len(pods.Items), strings.Join(notReady, "; "))
	default:
		// Partially ready is degraded rather than down: with raft HA a quorum of
		// unsealed members still serves reads, but the cluster has lost headroom.
		return Warnf("%d/%d pods Ready: %s", len(ready), len(pods.Items), strings.Join(notReady, "; "))
	}
}

func podIsReady(pod corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// checkStatefulSetRevisions flags StatefulSets whose applied revision has not
// been adopted by their pods.
//
// Under updateStrategy: OnDelete the controller records a new updateRevision but
// never rolls pods, so an applied image or config change can sit unadopted
// indefinitely while everything else reads green — ArgoCD's StatefulSet health
// check treats OnDelete as healthy unconditionally, and .spec.updateStrategy
// sits in ignoreDifferences so a git diff will not show it either. This is the
// only signal that distinguishes "applied" from "running".
//
// Adoption is measured by updatedReplicas, not by currentRevision. Under
// OnDelete the controller never advances .status.currentRevision, even once
// every pod is running updateRevision — so comparing the two revisions reports
// a permanent skew on any OnDelete StatefulSet, which would train readers to
// ignore exactly the signal this check exists to raise. updatedReplicas counts
// pods whose controller-revision-hash equals updateRevision, which is the
// question actually being asked.
func checkStatefulSetRevisions(ctx context.Context, kube kubernetes.Interface) Result {
	sets, err := kube.AppsV1().StatefulSets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Failf("list statefulsets: %v", err)
	}
	if len(sets.Items) == 0 {
		return Pass("no StatefulSets found")
	}

	var pending []string
	for _, sts := range sets.Items {
		// No revision recorded yet (mid-creation), or scaled to zero: nothing to
		// adopt either way.
		if sts.Status.UpdateRevision == "" || sts.Status.Replicas == 0 {
			continue
		}
		stale := sts.Status.Replicas - sts.Status.UpdatedReplicas
		if stale <= 0 {
			continue
		}
		pending = append(pending, fmt.Sprintf("%s/%s [%s] %d/%d pods on %s",
			sts.Namespace, sts.Name, sts.Spec.UpdateStrategy.Type,
			sts.Status.UpdatedReplicas, sts.Status.Replicas,
			revisionHash(sts.Status.UpdateRevision)))
	}

	if len(pending) == 0 {
		return Passf("all %d StatefulSets fully rolled", len(sets.Items))
	}
	return Warnf("%d/%d pending roll: %s", len(pending), len(sets.Items), strings.Join(pending, "; "))
}

// revisionHash trims the owner-name prefix from a ControllerRevision name,
// leaving the hash suffix that actually distinguishes two revisions.
func revisionHash(rev string) string {
	if i := strings.LastIndex(rev, "-"); i >= 0 && i < len(rev)-1 {
		return rev[i+1:]
	}
	return rev
}

func checkGatewayClassAccepted(ctx context.Context, dyn dynamic.Interface, name string) Result {
	obj, err := dyn.Resource(gvrGatewayClass).Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return Fail("GatewayClass not found")
	}
	if err != nil {
		return Failf("get error: %v", err)
	}
	ok, msg := conditionStatus(obj, "Accepted")
	if ok {
		return Pass("Accepted")
	}
	return Failf("not Accepted: %s", msg)
}

func checkGatewayProgrammed(ctx context.Context, dyn dynamic.Interface, ns, name string) Result {
	obj, err := dyn.Resource(gvrGateway).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return Fail("Gateway not found")
	}
	if err != nil {
		return Failf("get error: %v", err)
	}
	ok, msg := conditionStatus(obj, "Programmed")
	if ok {
		return Pass("Programmed")
	}
	return Failf("not Programmed: %s", msg)
}

func checkHTTPRouteAccepted(ctx context.Context, dyn dynamic.Interface, ns, name string) Result {
	obj, err := dyn.Resource(gvrHTTPRoute).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return Fail("HTTPRoute not found")
	}
	if err != nil {
		return Failf("get error: %v", err)
	}
	parents, _, _ := unstructured.NestedSlice(obj.Object, "status", "parents")
	for _, p := range parents {
		parent, ok := p.(map[string]any)
		if !ok {
			continue
		}
		conditions, ok := parent["conditions"].([]any)
		if !ok {
			continue
		}
		for _, c := range conditions {
			cond, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if cond["type"] == "Accepted" {
				if cond["status"] == "True" {
					return Pass("Accepted")
				}
				msg, _ := cond["message"].(string)
				reason, _ := cond["reason"].(string)
				return Failf("not Accepted (%s): %s", reason, msg)
			}
		}
	}
	return Warn("no parent status yet")
}

func checkClusterSecretStoreReady(ctx context.Context, dyn dynamic.Interface, name string) Result {
	obj, err := dyn.Resource(gvrClusterSecretStore).Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return Fail("ClusterSecretStore not found")
	}
	if err != nil {
		return Failf("get error: %v", err)
	}
	ok, msg := conditionStatus(obj, "Ready")
	if ok {
		return Pass("Ready")
	}
	return Failf("not Ready: %s", msg)
}

func checkExternalSecretSynced(ctx context.Context, dyn dynamic.Interface, ns, name string) Result {
	obj, err := dyn.Resource(gvrExternalSecret).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return Fail("ExternalSecret not found")
	}
	if err != nil {
		return Failf("get error: %v", err)
	}
	ok, msg := conditionStatus(obj, "Ready")
	if ok {
		return Pass("Synced")
	}
	return Failf("not synced: %s", msg)
}

func checkCertificateReady(ctx context.Context, dyn dynamic.Interface, ns, name string) Result {
	obj, err := dyn.Resource(gvrCertificate).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return Fail("Certificate not found")
	}
	if err != nil {
		return Failf("get error: %v", err)
	}
	ok, msg := conditionStatus(obj, "Ready")
	if ok {
		return Pass("Ready")
	}
	return Warnf("not Ready: %s", msg)
}

func checkAllApplicationsHealthy(ctx context.Context, dyn dynamic.Interface) Result {
	list, err := dyn.Resource(gvrApplication).Namespace("argocd").List(ctx, metav1.ListOptions{})
	if err != nil {
		return Failf("list applications: %v", err)
	}
	var degraded, unsynced []string
	for _, item := range list.Items {
		name := item.GetName()
		syncStatus, _, _ := unstructured.NestedString(item.Object, "status", "sync", "status")
		healthStatus, _, _ := unstructured.NestedString(item.Object, "status", "health", "status")
		if healthStatus == "Degraded" || healthStatus == "Missing" {
			degraded = append(degraded, name+"("+healthStatus+")")
		} else if syncStatus == "OutOfSync" {
			unsynced = append(unsynced, name)
		}
	}
	total := len(list.Items)
	if len(degraded) > 0 {
		return Failf("%d/%d degraded: %s", len(degraded), total, strings.Join(degraded, ", "))
	}
	if len(unsynced) > 0 {
		return Warnf("%d/%d out-of-sync: %s", len(unsynced), total, strings.Join(unsynced, ", "))
	}
	return Passf("all %d apps Synced+Healthy", total)
}

// checkArgoWorkloadImageDrift flags workloads whose ArgoCD-managed pod
// template declares one image tag while the pods actually observed running
// report another.
//
// ArgoCD marks a workload Synced the moment its spec is applied to the API
// server — that is a declaration, not an observation. A StatefulSet under
// OnDelete, a Deployment stuck mid-rollout, or a DaemonSet whose old pods
// were never evicted can all sit "Synced" indefinitely while a real pod
// keeps serving the previous image, and none of that shows up in sync or
// health status. Each workload's own pods are read by its own selector
// rather than by an ArgoCD tracking label: this cluster's tracking mode is
// annotation-based (visible on managed resources' `argocd.argoproj.io/
// tracking-id` annotation), so pods are not guaranteed to carry the
// `app.kubernetes.io/instance` label a label-based install would rely on.
// This generalizes the lesson checkStatefulSetRevisions encodes — measure
// adoption by what is running — across every workload kind an Application
// manages, not only StatefulSets.
func checkArgoWorkloadImageDrift(ctx context.Context, kube kubernetes.Interface, dyn dynamic.Interface) Result {
	apps, err := dyn.Resource(gvrApplication).Namespace("argocd").List(ctx, metav1.ListOptions{})
	if err != nil {
		return Failf("list applications: %v", err)
	}

	var drifted []string
	checked := 0
	for _, app := range apps.Items {
		resources, _, _ := unstructured.NestedSlice(app.Object, "status", "resources")
		for _, raw := range resources {
			res, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			kind, _ := res["kind"].(string)
			ns, _ := res["namespace"].(string)
			name, _ := res["name"].(string)
			if ns == "" || name == "" {
				continue
			}

			declared, selector, err := workloadTemplate(ctx, kube, kind, ns, name)
			if err != nil || len(declared) == 0 || len(selector) == 0 {
				// Not an image-bearing workload kind, or it no longer exists —
				// either way there is nothing to compare, not a failure.
				continue
			}

			pods, err := kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
				LabelSelector: labels.SelectorFromSet(selector).String(),
			})
			if err != nil || len(pods.Items) == 0 {
				continue // nothing running to observe, e.g. scaled to zero
			}
			checked++
			running := map[string][]string{}
			for _, pod := range pods.Items {
				for _, cs := range pod.Status.ContainerStatuses {
					repo, tag := splitImageRef(cs.Image)
					if !containsString(running[repo], tag) {
						running[repo] = append(running[repo], tag)
					}
				}
			}

			for _, image := range declared {
				repo, tag := splitImageRef(image)
				runningTags, seen := running[repo]
				if !seen {
					continue // no running container observed for this image; not comparable
				}
				if !containsString(runningTags, tag) {
					drifted = append(drifted, fmt.Sprintf("%s/%s (%s): declared %s, running %s",
						ns, name, kind, tag, strings.Join(runningTags, ",")))
				}
			}
		}
	}

	if checked == 0 {
		return Pass("no ArgoCD-managed workload had running pods available to verify")
	}
	if len(drifted) == 0 {
		return Passf("declared image observed running on all %d verified workload(s)", checked)
	}
	return Warnf("%d workload(s) running a different tag than declared: %s", len(drifted), strings.Join(drifted, "; "))
}

// workloadTemplate returns the declared container images and pod selector for
// the named Deployment, StatefulSet, or DaemonSet. Any other kind returns no
// images, which the caller treats as nothing to compare rather than a failure.
func workloadTemplate(ctx context.Context, kube kubernetes.Interface, kind, ns, name string) ([]string, map[string]string, error) {
	switch kind {
	case "Deployment":
		d, err := kube.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, nil, err
		}
		return containerImages(d.Spec.Template.Spec.Containers), selectorLabels(d.Spec.Selector), nil
	case "StatefulSet":
		s, err := kube.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, nil, err
		}
		return containerImages(s.Spec.Template.Spec.Containers), selectorLabels(s.Spec.Selector), nil
	case "DaemonSet":
		ds, err := kube.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, nil, err
		}
		return containerImages(ds.Spec.Template.Spec.Containers), selectorLabels(ds.Spec.Selector), nil
	default:
		return nil, nil, nil
	}
}

func selectorLabels(sel *metav1.LabelSelector) map[string]string {
	if sel == nil {
		return nil
	}
	return sel.MatchLabels
}

func containerImages(containers []corev1.Container) []string {
	images := make([]string, 0, len(containers))
	for _, c := range containers {
		images = append(images, c.Image)
	}
	return images
}

// splitImageRef separates an image reference into a normalized repository and
// tag, dropping any digest suffix and any Docker Hub prefix the container
// runtime adds when reporting what is actually running. containerd
// canonicalizes unqualified Docker Hub references on report-back — e.g. a pod
// template asking for "redis:7-alpine" comes back from the kubelet as
// "docker.io/library/redis:7-alpine" — even though the workload spec that
// requested the pull never had that prefix. Comparing the raw strings would
// flag every unqualified image as permanently drifted.
func splitImageRef(ref string) (repo, tag string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	repo = ref
	for _, prefix := range []string{"index.docker.io/library/", "index.docker.io/", "docker.io/library/", "docker.io/"} {
		if trimmed := strings.TrimPrefix(repo, prefix); trimmed != repo {
			repo = trimmed
			break
		}
	}
	if i := strings.LastIndex(repo, ":"); i >= 0 && !strings.Contains(repo[i:], "/") {
		return repo[:i], repo[i+1:]
	}
	return repo, ""
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// resourceDefault names one field (requests or limits) a LimitRange
// auto-populates for a container that omits it.
type resourceDefault struct {
	resource corev1.ResourceName
	field    string // "requests" or "limits"
}

// containerDefaults returns the (resource, field) pairs a Container-scoped
// LimitRange entry defaults. Pod-scoped entries are ignored: they cap sums
// across a pod's containers rather than defaulting any single container's
// spec, so they carry nothing for this per-container comparison.
func containerDefaults(lr corev1.LimitRange) []resourceDefault {
	var defaults []resourceDefault
	for _, item := range lr.Spec.Limits {
		if item.Type != corev1.LimitTypeContainer {
			continue
		}
		for res := range item.DefaultRequest {
			defaults = append(defaults, resourceDefault{res, "requests"})
		}
		for res := range item.Default {
			defaults = append(defaults, resourceDefault{res, "limits"})
		}
	}
	return defaults
}

func hasResourceField(res corev1.ResourceRequirements, d resourceDefault) bool {
	if d.field == "limits" {
		_, ok := res.Limits[d.resource]
		return ok
	}
	_, ok := res.Requests[d.resource]
	return ok
}

// checkLimitRangeAdoption flags pod containers that predate their
// namespace's LimitRange and therefore never received the field it defaults.
//
// A LimitRange only defaults resources.requests/limits at admission — it
// mutates a pod's spec once, on create, and nothing reconciles that spec
// afterward. A pod admitted before the LimitRange existed keeps running with
// whatever (or nothing) it was given at the time, indefinitely, and nothing
// about it looks wrong: ArgoCD reports the LimitRange itself Synced, because
// the LimitRange resource applied fine — it is the untouched pods that
// drifted from the policy's intent. Comparing each pod's own
// CreationTimestamp against the LimitRange's is the only way to tell "should
// have been defaulted" from "was defaulted and nothing is wrong."
func checkLimitRangeAdoption(ctx context.Context, kube kubernetes.Interface) Result {
	limitRanges, err := kube.CoreV1().LimitRanges(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Failf("list limitranges: %v", err)
	}
	if len(limitRanges.Items) == 0 {
		return Pass("no LimitRanges found")
	}

	var stale []staleContainer
	checked := 0
	for _, lr := range limitRanges.Items {
		defaults := containerDefaults(lr)
		if len(defaults) == 0 {
			continue
		}
		pods, err := kube.CoreV1().Pods(lr.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return Failf("list pods in %s: %v", lr.Namespace, err)
		}
		checked++
		for _, pod := range pods.Items {
			if !pod.CreationTimestamp.Time.Before(lr.CreationTimestamp.Time) {
				continue // admitted with the LimitRange already in effect
			}
			for _, c := range pod.Spec.Containers {
				for _, d := range defaults {
					if !hasResourceField(c.Resources, d) {
						stale = append(stale, staleContainer{
							namespace:  pod.Namespace,
							pod:        pod.Name,
							container:  c.Name,
							field:      fmt.Sprintf("%s.%s", d.field, d.resource),
							limitRange: lr.Name,
						})
					}
				}
			}
		}
	}

	if checked == 0 {
		return Pass("no LimitRanges define container-level defaults")
	}
	if len(stale) == 0 {
		return Passf("all pods in %d LimitRange-governed namespace(s) hold the defaulted fields", checked)
	}
	return Warnf("%d container(s) predate their namespace LimitRange and lack the defaulted field: %s",
		len(stale), summarizeStale(stale))
}

// staleContainer is one container missing a field its namespace LimitRange
// would have defaulted had the pod been admitted after it.
type staleContainer struct {
	namespace  string
	pod        string
	container  string
	field      string
	limitRange string
}

// summarizeStale collapses identically-broken containers into one entry per
// (namespace, container, field), naming a single example pod and a count.
// Replicas of the same workload are one remediation, not N, and listing every
// pod produced a line long enough to swamp the rest of the report — while
// naming none would leave nothing concrete to act on.
func summarizeStale(stale []staleContainer) string {
	type group struct {
		key   staleContainer
		count int
	}
	var order []string
	groups := map[string]*group{}
	for _, s := range stale {
		k := s.namespace + "|" + s.container + "|" + s.field + "|" + s.limitRange
		if g, ok := groups[k]; ok {
			g.count++
			continue
		}
		groups[k] = &group{key: s, count: 1}
		order = append(order, k)
	}

	parts := make([]string, 0, len(order))
	for _, k := range order {
		g := groups[k]
		entry := fmt.Sprintf("%s/%s missing %s (predates limitrange/%s, e.g. pod %s",
			g.key.namespace, g.key.container, g.key.field, g.key.limitRange, g.key.pod)
		if g.count > 1 {
			entry += fmt.Sprintf(" and %d more", g.count-1)
		}
		parts = append(parts, entry+")")
	}
	return strings.Join(parts, "; ")
}

// conditionStatus reads status.conditions[type=condType] from an unstructured object.
// Returns (true, "") when status==True, or (false, message) otherwise.
func conditionStatus(obj *unstructured.Unstructured, condType string) (bool, string) {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if cond["type"] == condType {
			msg, _ := cond["message"].(string)
			return cond["status"] == "True", msg
		}
	}
	return false, "condition not found"
}
