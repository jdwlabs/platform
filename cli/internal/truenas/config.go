package truenas

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

// Storage class names, and the provisioners that back them.
const (
	ClassISCSI = "truenas-iscsi"
	ClassNFS   = "truenas-nfs"

	ProvisionerISCSI = "org.democratic-csi.iscsi"
	ProvisionerNFS   = "org.democratic-csi.nfs"

	// DriverNamespace holds both democratic-csi releases and the driver-config
	// Secrets External Secrets renders from kv/truenas-csi.
	DriverNamespace = "democratic-csi"

	iscsiConfigSecret = "democratic-csi-iscsi-driver-config"
	nfsConfigSecret   = "democratic-csi-driver-config"
	configSecretKey   = "driver-config-file.yaml"

	// APIKeyEnv and AddrEnv let a run that has no cluster access supply the
	// same two values by hand, mirroring how the gitsync commands accept
	// Grafana credentials from the environment.
	APIKeyEnv = "PLATFORMCTL_TRUENAS_API_KEY"
	AddrEnv   = "PLATFORMCTL_TRUENAS_ADDR"

	// The middleware listens for TLS on 443 regardless of the plaintext port
	// the CSI drivers were configured with, so the driver config's port is
	// deliberately not reused for this connection.
	middlewarePort = "443"
)

// Classes returns the storage classes this backend covers, in report order.
func Classes() []string { return []string{ClassISCSI, ClassNFS} }

// DriverConfig is the part of one rendered democratic-csi driver config the
// reclaim path needs.
//
// Reading it from the Secret rather than hard-coding anything is the point:
// the NAS address, the dataset parents and the iSCSI naming convention are all
// decided by that file, so a config change moves this command with it instead
// of silently pointing it at the wrong subtree.
type DriverConfig struct {
	StorageClass string
	Provisioner  string
	Host         string
	// DatasetParent is zfs.datasetParentName — the only subtree this command
	// will ever consider for deletion.
	DatasetParent string
	// SnapshotParent is zfs.detachedSnapshotsDatasetParentName. It is recorded
	// so the classifier can recognise, and refuse, anything living under it.
	SnapshotParent string
	TargetPrefix   string
	TargetSuffix   string

	apiKey string
}

// APIKey returns the credential. It is unexported in the struct so that no
// formatting verb, log line or JSON encoder can reach it by accident; every
// read is an explicit call that shows up in review.
func (c DriverConfig) APIKey() string { return c.apiKey }

// Endpoint is the host:port the middleware is reached on. A host that already
// carries a port keeps it — an operator pointing PLATFORMCTL_TRUENAS_ADDR at a
// NAS behind a different TLS port means that port, not that port plus a default
// appended to it.
func (c DriverConfig) Endpoint() string {
	if _, _, err := net.SplitHostPort(c.Host); err == nil {
		return c.Host
	}
	return net.JoinHostPort(c.Host, middlewarePort)
}

// LoadDriverConfigs reads both rendered driver configs. A class whose Secret is
// absent is reported as an error rather than skipped: a missing config means
// the command would silently cover one class while claiming to cover two.
func LoadDriverConfigs(ctx context.Context, kube kubernetes.Interface, classes []string) ([]DriverConfig, error) {
	out := make([]DriverConfig, 0, len(classes))
	for _, class := range classes {
		cfg, err := loadDriverConfig(ctx, kube, class)
		if err != nil {
			return nil, err
		}
		out = append(out, cfg)
	}
	return out, nil
}

func loadDriverConfig(ctx context.Context, kube kubernetes.Interface, class string) (DriverConfig, error) {
	secretName, provisioner, err := secretForClass(class)
	if err != nil {
		return DriverConfig{}, err
	}

	sec, err := kube.CoreV1().Secrets(DriverNamespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return DriverConfig{}, fmt.Errorf(
				"secret %s/%s does not exist, so the %s driver config cannot be read",
				DriverNamespace, secretName, class)
		}
		return DriverConfig{}, fmt.Errorf("read secret %s/%s: %w", DriverNamespace, secretName, err)
	}
	raw, ok := sec.Data[configSecretKey]
	if !ok {
		return DriverConfig{}, fmt.Errorf("secret %s/%s carries no %s key",
			DriverNamespace, secretName, configSecretKey)
	}

	cfg, err := ParseDriverConfig(class, provisioner, raw)
	if err != nil {
		return DriverConfig{}, fmt.Errorf("secret %s/%s: %w", DriverNamespace, secretName, err)
	}
	return applyEnvOverrides(cfg), nil
}

func secretForClass(class string) (secretName, provisioner string, err error) {
	switch class {
	case ClassISCSI:
		return iscsiConfigSecret, ProvisionerISCSI, nil
	case ClassNFS:
		return nfsConfigSecret, ProvisionerNFS, nil
	default:
		return "", "", fmt.Errorf("unknown storage class %s; valid: %s", class, strings.Join(Classes(), ", "))
	}
}

// ParseDriverConfig reads the rendered driver-config-file.yaml.
func ParseDriverConfig(class, provisioner string, raw []byte) (DriverConfig, error) {
	var doc struct {
		Driver         string `json:"driver"`
		HTTPConnection struct {
			Host   string `json:"host"`
			APIKey string `json:"apiKey"`
		} `json:"httpConnection"`
		ZFS struct {
			DatasetParentName                  string `json:"datasetParentName"`
			DetachedSnapshotsDatasetParentName string `json:"detachedSnapshotsDatasetParentName"`
		} `json:"zfs"`
		ISCSI struct {
			NamePrefix string `json:"namePrefix"`
			NameSuffix string `json:"nameSuffix"`
		} `json:"iscsi"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return DriverConfig{}, fmt.Errorf("parse %s: %w", configSecretKey, err)
	}

	cfg := DriverConfig{
		StorageClass:   class,
		Provisioner:    provisioner,
		Host:           doc.HTTPConnection.Host,
		DatasetParent:  strings.Trim(doc.ZFS.DatasetParentName, "/"),
		SnapshotParent: strings.Trim(doc.ZFS.DetachedSnapshotsDatasetParentName, "/"),
		TargetPrefix:   doc.ISCSI.NamePrefix,
		TargetSuffix:   doc.ISCSI.NameSuffix,
		apiKey:         doc.HTTPConnection.APIKey,
	}
	if cfg.Host == "" {
		return DriverConfig{}, fmt.Errorf("httpConnection.host is empty")
	}
	// An empty parent would make every dataset on the pool a match, so this is
	// a refusal rather than a default.
	if cfg.DatasetParent == "" {
		return DriverConfig{}, fmt.Errorf("zfs.datasetParentName is empty")
	}
	return cfg, nil
}

func applyEnvOverrides(cfg DriverConfig) DriverConfig {
	if addr := os.Getenv(AddrEnv); addr != "" {
		cfg.Host = addr
	}
	if key := os.Getenv(APIKeyEnv); key != "" {
		cfg.apiKey = key
	}
	return cfg
}

// NewDriverConfigForTest builds a config without a cluster read.
func NewDriverConfigForTest(class, host, datasetParent, apiKey string) DriverConfig {
	provisioner := ProvisionerNFS
	if class == ClassISCSI {
		provisioner = ProvisionerISCSI
	}
	return DriverConfig{
		StorageClass:  class,
		Provisioner:   provisioner,
		Host:          host,
		DatasetParent: datasetParent,
		apiKey:        apiKey,
	}
}

// parseSize reads the numeric forms the middleware uses for byte counts. A
// value it cannot read is an error rather than a zero: a silent 0 reports a
// reclaim as freeing nothing, which reads as a successful no-op.
func parseSize(raw any) (int64, error) {
	switch v := raw.(type) {
	case nil:
		return 0, nil
	case float64:
		return int64(v), nil
	case int64:
		return v, nil
	case string:
		if v == "" {
			return 0, nil
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("size %q is not an integer", v)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("size is %T, want a number", raw)
	}
}
