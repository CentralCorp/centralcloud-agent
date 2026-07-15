package metrics

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	Up, DeploymentsTotal, DeploymentsActive, DockerHealth, PostgresHealth, AvailableMemory, AvailableDisk prometheus.Gauge
	Operations, OperationFailures                                                                         *prometheus.CounterVec
	OperationDuration                                                                                     *prometheus.HistogramVec
}

func New(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Up:               prometheus.NewGauge(prometheus.GaugeOpts{Name: "centralcloud_agent_up", Help: "Whether the agent is running."}),
		DeploymentsTotal: prometheus.NewGauge(prometheus.GaugeOpts{Name: "centralcloud_deployments_total", Help: "Managed deployments."}), DeploymentsActive: prometheus.NewGauge(prometheus.GaugeOpts{Name: "centralcloud_deployments_active", Help: "Active deployments."}),
		Operations: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "centralcloud_operations_total", Help: "Operations by type."}, []string{"type"}), OperationFailures: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "centralcloud_operations_failed_total", Help: "Failed operations by type."}, []string{"type"}), OperationDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "centralcloud_operation_duration_seconds", Help: "Operation duration.", Buckets: prometheus.DefBuckets}, []string{"type"}),
		DockerHealth: prometheus.NewGauge(prometheus.GaugeOpts{Name: "centralcloud_docker_health", Help: "Docker health."}), PostgresHealth: prometheus.NewGauge(prometheus.GaugeOpts{Name: "centralcloud_postgres_health", Help: "PostgreSQL health."}), AvailableMemory: prometheus.NewGauge(prometheus.GaugeOpts{Name: "centralcloud_available_memory_bytes", Help: "Available memory."}), AvailableDisk: prometheus.NewGauge(prometheus.GaugeOpts{Name: "centralcloud_available_disk_bytes", Help: "Available disk."}),
	}
	reg.MustRegister(m.Up, m.DeploymentsTotal, m.DeploymentsActive, m.Operations, m.OperationFailures, m.OperationDuration, m.DockerHealth, m.PostgresHealth, m.AvailableMemory, m.AvailableDisk)
	m.Up.Set(1)
	return m
}
