package tenants

// Tenant mirrors the on-disk tenant.yaml structure.
type Tenant struct {
	Name       string      `yaml:"name" json:"name"`
	Namespaces []Namespace `yaml:"namespaces" json:"namespaces"`
	Services   []Service   `yaml:"services" json:"services"`
}

type Namespace struct {
	Name string `yaml:"name" json:"name"`
}

type Service struct {
	Name        string `yaml:"name" json:"name"`
	Chart       string `yaml:"chart,omitempty" json:"chart,omitempty"`
	ChartPath   string `yaml:"chartPath,omitempty" json:"chartPath,omitempty"`
	Repo        string `yaml:"repo" json:"repo"`
	Revision    string `yaml:"revision" json:"revision"`
	Namespace   string `yaml:"namespace" json:"namespace"`
	PostInstall *bool  `yaml:"postInstall" json:"postInstall"`
	SyncWave    *int   `yaml:"syncWave" json:"syncWave"`
}
