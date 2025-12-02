// Package reconciler handles infrastructure reconciliation with Kubernetes
package reconciler

import (
	"fmt"
	"strings"

	agentv1 "github.com/calyra-tenatrix/agent/internal/proto/agent/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// ManifestGenerator generates Kubernetes manifests from ServiceSpec
type ManifestGenerator struct {
	namespace    string
	appLabel     string // tenatrix.io/app
	envLabel     string // tenatrix.io/env
	managedLabel string // tenatrix.io/managed
}

// NewManifestGenerator creates a new ManifestGenerator
func NewManifestGenerator(namespace string) *ManifestGenerator {
	return &ManifestGenerator{
		namespace:    namespace,
		appLabel:     "tenatrix.io/app",
		envLabel:     "tenatrix.io/env",
		managedLabel: "tenatrix.io/managed",
	}
}

// GenerateDeployment creates a Kubernetes Deployment from ServiceSpec
func (g *ManifestGenerator) GenerateDeployment(
	spec *agentv1.ServiceSpec,
	appID string,
	env string,
) *appsv1.Deployment {
	// Labels for selection and identification
	labels := g.createLabels(spec, appID, env)

	// Pod template labels (same as selector)
	podLabels := make(map[string]string)
	for k, v := range labels {
		podLabels[k] = v
	}

	// Add custom labels from spec
	for k, v := range spec.Labels {
		podLabels[k] = v
	}

	// Container ports
	containerPorts := g.convertPorts(spec.Ports)

	// Environment variables
	envVars := g.convertEnvVars(spec.Env)

	// Resource limits
	resources := g.convertResources(spec.ResourceLimits)

	// Replicas
	replicas := spec.Replicas
	if replicas == 0 {
		replicas = 1
	}

	// Volume mounts
	volumeMounts, volumes := g.convertVolumes(spec.Volumes)

	// Container definition
	container := corev1.Container{
		Name:            spec.Name,
		Image:           spec.Image,
		Ports:           containerPorts,
		Env:             envVars,
		Resources:       resources,
		VolumeMounts:    volumeMounts,
		ImagePullPolicy: corev1.PullIfNotPresent,
	}

	// Health check
	if spec.HealthCheck != nil {
		container.LivenessProbe = g.convertHealthCheck(spec.HealthCheck)
		container.ReadinessProbe = g.convertHealthCheck(spec.HealthCheck)
	}

	// Deployment
	deployment := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      g.deploymentName(spec),
			Namespace: g.namespace,
			Labels:    labels,
			Annotations: map[string]string{
				"tenatrix.io/service-id": spec.Id,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(replicas),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": spec.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: podLabels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{container},
					Volumes:    volumes,
				},
			},
		},
	}

	return deployment
}

// GenerateService creates a Kubernetes Service from ServiceSpec
func (g *ManifestGenerator) GenerateService(
	spec *agentv1.ServiceSpec,
	appID string,
	env string,
) *corev1.Service {
	labels := g.createLabels(spec, appID, env)

	// Service ports
	servicePorts := make([]corev1.ServicePort, 0)
	for i, p := range spec.Ports {
		protocol := corev1.ProtocolTCP
		if strings.ToUpper(p.Protocol) == "UDP" {
			protocol = corev1.ProtocolUDP
		}

		servicePorts = append(servicePorts, corev1.ServicePort{
			Name:       fmt.Sprintf("port-%d", i),
			Port:       p.ContainerPort,
			TargetPort: intstr.FromInt(int(p.ContainerPort)),
			Protocol:   protocol,
		})
	}

	// If no ports defined, don't create service
	if len(servicePorts) == 0 {
		return nil
	}

	service := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      g.serviceName(spec),
			Namespace: g.namespace,
			Labels:    labels,
			Annotations: map[string]string{
				"tenatrix.io/service-id": spec.Id,
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"app": spec.Name,
			},
			Ports: servicePorts,
			Type:  corev1.ServiceTypeClusterIP,
		},
	}

	return service
}

// ==================== Helper Methods ====================

// createLabels creates standard labels for Kubernetes resources
func (g *ManifestGenerator) createLabels(spec *agentv1.ServiceSpec, appID, env string) map[string]string {
	return map[string]string{
		"app":            spec.Name,
		g.managedLabel:   "true",
		g.appLabel:       appID,
		g.envLabel:       env,
		"tenatrix.io/id": spec.Id,
	}
}

// deploymentName generates deployment name
func (g *ManifestGenerator) deploymentName(spec *agentv1.ServiceSpec) string {
	return fmt.Sprintf("%s", spec.Name)
}

// serviceName generates service name
func (g *ManifestGenerator) serviceName(spec *agentv1.ServiceSpec) string {
	return fmt.Sprintf("%s", spec.Name)
}

// convertPorts converts proto PortMapping to Kubernetes ContainerPort
func (g *ManifestGenerator) convertPorts(ports []*agentv1.PortMapping) []corev1.ContainerPort {
	result := make([]corev1.ContainerPort, 0, len(ports))
	for i, p := range ports {
		protocol := corev1.ProtocolTCP
		if strings.ToUpper(p.Protocol) == "UDP" {
			protocol = corev1.ProtocolUDP
		}

		result = append(result, corev1.ContainerPort{
			Name:          fmt.Sprintf("port-%d", i),
			ContainerPort: p.ContainerPort,
			Protocol:      protocol,
		})
	}
	return result
}

// convertEnvVars converts map to Kubernetes EnvVar slice
func (g *ManifestGenerator) convertEnvVars(env map[string]string) []corev1.EnvVar {
	result := make([]corev1.EnvVar, 0, len(env))
	for k, v := range env {
		result = append(result, corev1.EnvVar{
			Name:  k,
			Value: v,
		})
	}
	return result
}

// convertResources converts proto ResourceLimits to Kubernetes ResourceRequirements
func (g *ManifestGenerator) convertResources(limits *agentv1.ResourceLimits) corev1.ResourceRequirements {
	if limits == nil {
		return corev1.ResourceRequirements{}
	}

	resources := corev1.ResourceRequirements{
		Limits:   corev1.ResourceList{},
		Requests: corev1.ResourceList{},
	}

	// CPU limit
	if limits.CpuLimit != "" {
		if cpu, err := resource.ParseQuantity(limits.CpuLimit); err == nil {
			resources.Limits[corev1.ResourceCPU] = cpu
		}
	}

	// Memory limit
	if limits.MemoryLimit != "" {
		if mem, err := resource.ParseQuantity(limits.MemoryLimit); err == nil {
			resources.Limits[corev1.ResourceMemory] = mem
		}
	}

	// CPU reservation (request)
	if limits.CpuReservation != "" {
		if cpu, err := resource.ParseQuantity(limits.CpuReservation); err == nil {
			resources.Requests[corev1.ResourceCPU] = cpu
		}
	}

	// Memory reservation (request)
	if limits.MemoryReservation != "" {
		if mem, err := resource.ParseQuantity(limits.MemoryReservation); err == nil {
			resources.Requests[corev1.ResourceMemory] = mem
		}
	}

	return resources
}

// convertVolumes converts proto VolumeMount to Kubernetes volumes
func (g *ManifestGenerator) convertVolumes(mounts []*agentv1.VolumeMount) ([]corev1.VolumeMount, []corev1.Volume) {
	volumeMounts := make([]corev1.VolumeMount, 0, len(mounts))
	volumes := make([]corev1.Volume, 0, len(mounts))

	for i, m := range mounts {
		volumeName := m.Name
		if volumeName == "" {
			volumeName = fmt.Sprintf("vol-%d", i)
		}

		// Volume mount
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      volumeName,
			MountPath: m.MountPath,
			SubPath:   m.SubPath,
			ReadOnly:  m.ReadOnly,
		})

		// Volume definition
		var volumeSource corev1.VolumeSource

		if m.PvcName != "" {
			// PersistentVolumeClaim
			volumeSource = corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: m.PvcName,
				},
			}
		} else if m.HostPath != "" {
			// HostPath volume
			volumeSource = corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: m.HostPath,
				},
			}
		} else {
			// EmptyDir (temporary volume)
			volumeSource = corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			}
		}

		volumes = append(volumes, corev1.Volume{
			Name:         volumeName,
			VolumeSource: volumeSource,
		})
	}

	return volumeMounts, volumes
}

// convertHealthCheck converts proto HealthCheckConfig to Kubernetes Probe
func (g *ManifestGenerator) convertHealthCheck(hc *agentv1.HealthCheckConfig) *corev1.Probe {
	probe := &corev1.Probe{
		InitialDelaySeconds: hc.InitialDelaySeconds,
		PeriodSeconds:       hc.PeriodSeconds,
		TimeoutSeconds:      hc.TimeoutSeconds,
		SuccessThreshold:    hc.SuccessThreshold,
		FailureThreshold:    hc.FailureThreshold,
	}

	// Set defaults if not specified
	if probe.PeriodSeconds == 0 {
		probe.PeriodSeconds = 10
	}
	if probe.TimeoutSeconds == 0 {
		probe.TimeoutSeconds = 3
	}
	if probe.SuccessThreshold == 0 {
		probe.SuccessThreshold = 1
	}
	if probe.FailureThreshold == 0 {
		probe.FailureThreshold = 3
	}

	switch hc.Type {
	case agentv1.HealthCheckType_HEALTH_CHECK_TYPE_HTTP:
		probe.ProbeHandler = corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: hc.HttpPath,
				Port: intstr.FromInt(int(hc.Port)),
			},
		}
	case agentv1.HealthCheckType_HEALTH_CHECK_TYPE_TCP:
		probe.ProbeHandler = corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{
				Port: intstr.FromInt(int(hc.Port)),
			},
		}
	case agentv1.HealthCheckType_HEALTH_CHECK_TYPE_EXEC:
		probe.ProbeHandler = corev1.ProbeHandler{
			Exec: &corev1.ExecAction{
				Command: hc.ExecCommand,
			},
		}
	default:
		return nil
	}

	return probe
}

// int32Ptr returns a pointer to an int32
func int32Ptr(i int32) *int32 {
	return &i
}
