# Terraform variables for platform infrastructure

# Node configuration
node_count = 3
node_type = "standard"

# Networking configuration
networking {
  flannel_backend = "vxlan"
  vxlan_port = 8472
  vxlan_vni = 100
}

# Kubernetes version
k8s_version = "v1.28.0"

# Cluster autoscaling
autoscaling_enabled = true
min_nodes = 3
max_nodes = 10

cpu_limit = "2"
memory_limit = "4Gi"

# Storage configuration
storage_class = "local-path"

# Monitoring and logging
monitoring_enabled = true
logging_enabled = true

# Security settings
rbac_enabled = true

# Platform components
platform_components = [
  "flannel",
  "coredns",
  "metrics-server",
  "ingress-nginx"
]

# Node labels and taints
node_labels = {
  "role" = "worker"
  "environment" = "production"
}

node_taints = [
  {
    key = "node-role.kubernetes.io/control-plane"
    effect = "NoSchedule"
  }
]

# Maintenance window
maintenance_window_start = "02:00"
maintenance_window_end = "06:00"

# Backup configuration
backup_enabled = true
backup_schedule = "0 2 * * *"

# Alerting rules
alerting_rules = [
  {
    name = "node-network-unavailable"
    severity = "warning"
    expression = "kube_node_status_network_unavailable > 0"
    duration = "5m"
  },
  {
    name = "flannel-pod-not-ready"
    severity = "critical"
    expression = "kube_pod_status_ready{pod='kube-flannel'} == 0"
    duration = "10m"
  }
]