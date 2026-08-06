package tenants

// Tenant mirrors the on-disk tenant.yaml structure.
type Tenant struct {
	Name          string         `yaml:"name" json:"name"`
	Namespaces    []Namespace    `yaml:"namespaces" json:"namespaces"`
	Services      []Service      `yaml:"services" json:"services"`
	Observability *Observability `yaml:"observability,omitempty" json:"observability,omitempty"`
}

// Observability is the optional per-tenant Grafana folder/team/RBAC and
// signal-store tenancy block. Absent entirely for tenants that don't yet
// have per-tenant observability (see
// docs/observability/DASHBOARDS-AND-MULTITENANCY.md §5.5).
type Observability struct {
	TenantID string          `yaml:"tenantId" json:"tenantId"`
	Grafana  ObsGrafanaBlock `yaml:"grafana" json:"grafana"`
}

type ObsGrafanaBlock struct {
	Folder string `yaml:"folder" json:"folder"`
	Team   string `yaml:"team" json:"team"`
	Access string `yaml:"access" json:"access"`
}

type Namespace struct {
	Name string `yaml:"name" json:"name"`
}

type Service struct {
	Name         string `yaml:"name" json:"name"`
	Chart        string `yaml:"chart,omitempty" json:"chart,omitempty"`
	ChartPath    string `yaml:"chartPath,omitempty" json:"chartPath,omitempty"`
	Repo         string `yaml:"repo" json:"repo"`
	Revision     string `yaml:"revision" json:"revision"`
	Namespace    string `yaml:"namespace" json:"namespace"`
	PostInstall  *bool  `yaml:"postInstall" json:"postInstall"`
	SyncWave     *int   `yaml:"syncWave" json:"syncWave"`
	RawManifests bool   `yaml:"rawManifests,omitempty" json:"rawManifests,omitempty"`
}
