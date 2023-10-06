/*
Copyright 2023 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package revision

import (
	"golang.org/x/net/context"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/crossplane/crossplane-runtime/pkg/meta"

	v1 "github.com/crossplane/crossplane/apis/pkg/v1"
	"github.com/crossplane/crossplane/apis/pkg/v1alpha1"
	"github.com/crossplane/crossplane/apis/pkg/v1beta1"
)

const (
	runtimeContainerName = "package-runtime"
	// Providers are expected to use port 8080 if they expose Prometheus
	// metrics, which any provider built using controller-runtime will do by
	// default.
	metricsPortName   = "metrics"
	metricsPortNumber = 8080

	webhookTLSCertDirEnvVar = "WEBHOOK_TLS_CERT_DIR"
	webhookPortName         = "webhook"

	// See https://github.com/grpc/grpc/blob/v1.58.0/doc/naming.md
	grpcPortName       = "grpc"
	servicePort        = 9443
	serviceEndpointFmt = "dns:///%s.%s:%d"

	essTLSCertDirEnvVar = "ESS_TLS_CERTS_DIR"

	tlsServerCertDirEnvVar   = "TLS_SERVER_CERTS_DIR"
	tlsServerCertsVolumeName = "tls-server-certs"
	tlsServerCertsDir        = "/tls/server"

	tlsClientCertDirEnvVar   = "TLS_CLIENT_CERTS_DIR"
	tlsClientCertsVolumeName = "tls-client-certs"
	tlsClientCertsDir        = "/tls/client"
)

const (
	errGetControllerConfig = "cannot get referenced controller config"
	errGetRuntimeConfig    = "cannot get referenced deployment runtime config"
	errGetServiceAccount   = "cannot get Crossplane service account"
)

// ManifestBuilder builds the runtime manifests for a package revision.
type ManifestBuilder interface {
	// ServiceAccount builds and returns the service account manifest.
	ServiceAccount(overrides ...ServiceAccountOverrides) *corev1.ServiceAccount
	// Deployment builds and returns the deployment manifest.
	Deployment(serviceAccount string, overrides ...DeploymentOverrides) *appsv1.Deployment
	// Service builds and returns the service manifest.
	Service(overrides ...ServiceOverrides) *corev1.Service
	// TLSClientSecret builds and returns the TLS client secret manifest.
	TLSClientSecret() *corev1.Secret
	// TLSServerSecret builds and returns the TLS server secret manifest.
	TLSServerSecret() *corev1.Secret
}

// A RuntimeHooks performs runtime operations before and after a revision
// establishes objects.
type RuntimeHooks interface {
	// Pre performs operations meant to happen before establishing objects.
	Pre(context.Context, runtime.Object, v1.PackageRevisionWithRuntime, ManifestBuilder) error

	// Post performs operations meant to happen after establishing objects.
	Post(context.Context, runtime.Object, v1.PackageRevisionWithRuntime, ManifestBuilder) error

	// Deactivate performs operations meant to happen before deactivating a revision.
	Deactivate(context.Context, v1.PackageRevisionWithRuntime, ManifestBuilder) error
}

// RuntimeManifestBuilder builds the runtime manifests for a package revision.
type RuntimeManifestBuilder struct {
	revision                  v1.PackageRevisionWithRuntime
	namespace                 string
	serviceAccountPullSecrets []corev1.LocalObjectReference
	controllerConfig          *v1alpha1.ControllerConfig
	runtimeConfig             *v1beta1.DeploymentRuntimeConfig
}

// NewRuntimeManifestBuilder returns a new RuntimeManifestBuilder.
func NewRuntimeManifestBuilder(ctx context.Context, client client.Client, namespace string, serviceAccount string, pwr v1.PackageRevisionWithRuntime) (*RuntimeManifestBuilder, error) {
	b := &RuntimeManifestBuilder{
		namespace: namespace,
		revision:  pwr,
	}

	if ccRef := pwr.GetControllerConfigRef(); ccRef != nil {
		cc := &v1alpha1.ControllerConfig{}
		if err := client.Get(ctx, types.NamespacedName{Name: ccRef.Name}, cc); err != nil {
			return nil, errors.Wrap(err, errGetControllerConfig)
		}
		b.controllerConfig = cc
	}

	if rcRef := pwr.GetRuntimeConfigRef(); rcRef != nil {
		rc := &v1beta1.DeploymentRuntimeConfig{}
		if err := client.Get(ctx, types.NamespacedName{Name: rcRef.Name}, rc); err != nil {
			return nil, errors.Wrap(err, errGetControllerConfig)
		}
		b.runtimeConfig = rc
	}

	sa := &corev1.ServiceAccount{}
	// Fetch XP ServiceAccount to get the ImagePullSecrets defined there.
	// We will append them to the list of ImagePullSecrets for the runtime
	// ServiceAccount.
	if err := client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: serviceAccount}, sa); err != nil {
		return nil, errors.Wrap(err, errGetServiceAccount)
	}
	b.serviceAccountPullSecrets = sa.ImagePullSecrets

	return b, nil
}

// ServiceAccount builds and returns the ServiceAccount manifest.
func (b *RuntimeManifestBuilder) ServiceAccount(overrides ...ServiceAccountOverrides) *corev1.ServiceAccount {
	sa := defaultServiceAccount(b.revision.GetName())

	var allOverrides []ServiceAccountOverrides
	allOverrides = append(allOverrides,
		// Currently it is not possible to override the namespace,
		// ownerReferences or pullSecrets of the service account, and we could
		// define them as defaults. However, we will leave them as overrides
		// to indicate that we are opinionated about them currently and follow
		// a consistent pattern.
		ServiceAccountWithNamespace(b.namespace),
		ServiceAccountWithOwnerReferences([]metav1.OwnerReference{meta.AsController(meta.TypedReferenceTo(b.revision, b.revision.GetObjectKind().GroupVersionKind()))}),
		ServiceAccountWithAdditionalPullSecrets(append(b.revision.GetPackagePullSecrets(), b.serviceAccountPullSecrets...)),
	)

	if cc := b.controllerConfig; cc != nil {
		allOverrides = append(allOverrides, ServiceAccountWithControllerConfig(cc))
	}

	// We append the overrides passed to the function last so that they can
	// override the above ones.
	allOverrides = append(allOverrides, overrides...)

	for _, o := range allOverrides {
		o(sa)
	}

	return sa
}

// Deployment builds and returns the Deployment manifest.
func (b *RuntimeManifestBuilder) Deployment(serviceAccount string, overrides ...DeploymentOverrides) *appsv1.Deployment {
	d := defaultDeployment(b.revision.GetName())

	var allOverrides []DeploymentOverrides
	allOverrides = append(allOverrides,
		DeploymentWithNamespace(b.namespace),
		DeploymentWithOwnerReferences([]metav1.OwnerReference{meta.AsController(meta.TypedReferenceTo(b.revision, b.revision.GetObjectKind().GroupVersionKind()))}),
		DeploymentWithSelectors(b.podSelectors()),
		DeploymentWithServiceAccount(serviceAccount),
		DeploymentWithImagePullSecrets(b.revision.GetPackagePullSecrets()),
		DeploymentRuntimeWithImage(b.revision.GetSource()),
	)

	if b.revision.GetPackagePullPolicy() != nil {
		// If the package pull policy is set, it will override the default
		// or whatever is set in the runtime config.
		allOverrides = append(allOverrides, DeploymentRuntimeWithImagePullPolicy(*b.revision.GetPackagePullPolicy()))
	}

	if b.revision.GetTLSClientSecretName() != nil {
		allOverrides = append(allOverrides, DeploymentRuntimeWithTLSClientSecret(*b.revision.GetTLSClientSecretName()))
	}

	if b.revision.GetTLSServerSecretName() != nil {
		allOverrides = append(allOverrides, DeploymentRuntimeWithTLSServerSecret(*b.revision.GetTLSServerSecretName()))
	}

	if b.controllerConfig != nil {
		allOverrides = append(allOverrides, DeploymentForControllerConfig(b.controllerConfig))
	}

	// We append the overrides passed to the function last so that they can
	// override the above ones.
	allOverrides = append(allOverrides, overrides...)

	for _, o := range allOverrides {
		o(d)
	}

	return d
}

// Service builds and returns the Service manifest.
func (b *RuntimeManifestBuilder) Service(overrides ...ServiceOverrides) *corev1.Service {
	svc := defaultService(b.packageName())

	var allOverrides []ServiceOverrides
	allOverrides = append(allOverrides,
		// Currently it is not possible to override the namespace,
		// ownerReferences, selectors or ports of the service, and we could
		// define them as defaults. However, we will leave them as overrides
		// to indicate that we are opinionated about them currently and follow
		// a consistent pattern.
		ServiceWithNamespace(b.namespace),
		ServiceWithOwnerReferences([]metav1.OwnerReference{meta.AsController(meta.TypedReferenceTo(b.revision, b.revision.GetObjectKind().GroupVersionKind()))}),
		ServiceWithSelectors(b.podSelectors()),
		ServiceWithAdditionalPorts([]corev1.ServicePort{
			{
				Protocol:   corev1.ProtocolTCP,
				Port:       servicePort,
				TargetPort: intstr.FromInt32(servicePort),
			},
		}))

	// We append the overrides passed to the function last so that they can
	// override the above ones.
	allOverrides = append(allOverrides, overrides...)

	for _, o := range allOverrides {
		o(svc)
	}

	return svc
}

// TLSClientSecret builds and returns the Secret manifest for the TLS client certificate.
func (b *RuntimeManifestBuilder) TLSClientSecret() *corev1.Secret {
	if b.revision.GetTLSClientSecretName() == nil {
		return nil
	}

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            *b.revision.GetTLSClientSecretName(),
			Namespace:       b.namespace,
			OwnerReferences: []metav1.OwnerReference{meta.AsController(meta.TypedReferenceTo(b.revision, b.revision.GetObjectKind().GroupVersionKind()))},
		},
	}
}

// TLSServerSecret builds and returns the Secret manifest for the TLS server certificate.
func (b *RuntimeManifestBuilder) TLSServerSecret() *corev1.Secret {
	if b.revision.GetTLSServerSecretName() == nil {
		return nil
	}

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            *b.revision.GetTLSServerSecretName(),
			Namespace:       b.namespace,
			OwnerReferences: []metav1.OwnerReference{meta.AsController(meta.TypedReferenceTo(b.revision, b.revision.GetObjectKind().GroupVersionKind()))},
		},
	}
}

func (b *RuntimeManifestBuilder) podSelectors() map[string]string {
	return map[string]string{
		"pkg.crossplane.io/revision":           b.revision.GetName(),
		"pkg.crossplane.io/" + b.packageType(): b.packageName(),
	}
}

func (b *RuntimeManifestBuilder) packageName() string {
	return b.revision.GetLabels()[v1.LabelParentPackage]
}

func (b *RuntimeManifestBuilder) packageType() string {
	if _, ok := b.revision.(*v1beta1.FunctionRevision); ok {
		return "function"
	}
	return "provider"
}
