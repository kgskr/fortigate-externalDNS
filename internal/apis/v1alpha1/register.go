package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const GroupName = "fortigate-external-dns.kgskr.io"

var (
	SchemeGroupVersion = schema.GroupVersion{Group: GroupName, Version: "v1alpha1"}
	SchemeBuilder      = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme        = SchemeBuilder.AddToScheme

	TargetGVR     = SchemeGroupVersion.WithResource("fortigatednstargets")
	OwnershipGVR  = SchemeGroupVersion.WithResource("fortigatednsrecordownerships")
	PolicyGVR     = SchemeGroupVersion.WithResource("fortigatednspolicies")
	ChangePlanGVR = SchemeGroupVersion.WithResource("fortigatednschangeplans")
	StatusGVR     = SchemeGroupVersion.WithResource("fortigatednsstatuses")
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemeGroupVersion,
		&FortiGateDNSTarget{}, &FortiGateDNSTargetList{},
		&FortiGateDNSRecordOwnership{}, &FortiGateDNSRecordOwnershipList{},
		&FortiGateDNSPolicy{}, &FortiGateDNSPolicyList{},
		&FortiGateDNSChangePlan{}, &FortiGateDNSChangePlanList{},
		&FortiGateDNSStatus{}, &FortiGateDNSStatusList{},
	)
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}
