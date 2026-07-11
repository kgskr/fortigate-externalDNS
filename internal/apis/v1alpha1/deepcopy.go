package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func copyStrings(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string(nil), in...)
}

func (in *FortiGateDNSTarget) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(FortiGateDNSTarget)
	*out = *in
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	out.Spec.Sources = copyStrings(in.Spec.Sources)
	out.Spec.Namespaces = copyStrings(in.Spec.Namespaces)
	out.Spec.GatewayTargetNamespaces = copyStrings(in.Spec.GatewayTargetNamespaces)
	out.Spec.DomainFilters = copyStrings(in.Spec.DomainFilters)
	if in.Spec.APITokenSecretRef.Optional != nil {
		optional := *in.Spec.APITokenSecretRef.Optional
		out.Spec.APITokenSecretRef.Optional = &optional
	}
	if in.Spec.CARef != nil {
		caRef := *in.Spec.CARef
		out.Spec.CARef = &caRef
	}
	out.Status.Conditions = append([]metav1.Condition(nil), in.Status.Conditions...)
	return out
}

func (in *FortiGateDNSTargetList) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(FortiGateDNSTargetList)
	*out = *in
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]FortiGateDNSTarget, len(in.Items))
		for i := range in.Items {
			out.Items[i] = *(in.Items[i].DeepCopyObject().(*FortiGateDNSTarget))
		}
	}
	return out
}

func (in *FortiGateDNSRecordOwnership) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(FortiGateDNSRecordOwnership)
	*out = *in
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	out.Spec.Sources = append([]SourceObjectReference(nil), in.Spec.Sources...)
	if in.Status.LastConfirmedTime != nil {
		t := in.Status.LastConfirmedTime.DeepCopy()
		out.Status.LastConfirmedTime = t
	}
	out.Status.Conditions = append([]metav1.Condition(nil), in.Status.Conditions...)
	return out
}

func (in *FortiGateDNSRecordOwnershipList) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(FortiGateDNSRecordOwnershipList)
	*out = *in
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]FortiGateDNSRecordOwnership, len(in.Items))
		for i := range in.Items {
			out.Items[i] = *(in.Items[i].DeepCopyObject().(*FortiGateDNSRecordOwnership))
		}
	}
	return out
}

func (in *FortiGateDNSPolicy) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(FortiGateDNSPolicy)
	*out = *in
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	if in.Spec.Selector != nil {
		out.Spec.Selector = in.Spec.Selector.DeepCopy()
	}
	out.Spec.SourceKinds = copyStrings(in.Spec.SourceKinds)
	out.Spec.AllowedHostnameSuffixes = copyStrings(in.Spec.AllowedHostnameSuffixes)
	out.Spec.AllowedTargetCIDRs = copyStrings(in.Spec.AllowedTargetCIDRs)
	out.Spec.AllowedTargetSuffixes = copyStrings(in.Spec.AllowedTargetSuffixes)
	if in.Spec.TTL != nil {
		ttl := *in.Spec.TTL
		out.Spec.TTL = &ttl
	}
	if in.Spec.RequireOptIn != nil {
		requirement := *in.Spec.RequireOptIn
		out.Spec.RequireOptIn = &requirement
	}
	return out
}

func (in *FortiGateDNSPolicyList) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(FortiGateDNSPolicyList)
	*out = *in
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]FortiGateDNSPolicy, len(in.Items))
		for i := range in.Items {
			out.Items[i] = *(in.Items[i].DeepCopyObject().(*FortiGateDNSPolicy))
		}
	}
	return out
}

func (in *FortiGateDNSChangePlan) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(FortiGateDNSChangePlan)
	*out = *in
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	if in.Spec.OwnershipResourceVersions != nil {
		out.Spec.OwnershipResourceVersions = make(map[string]string, len(in.Spec.OwnershipResourceVersions))
		for key, value := range in.Spec.OwnershipResourceVersions {
			out.Spec.OwnershipResourceVersions[key] = value
		}
	}
	if in.Spec.Operations != nil {
		out.Spec.Operations = make([]PlanOperation, len(in.Spec.Operations))
		copy(out.Spec.Operations, in.Spec.Operations)
		for i := range in.Spec.Operations {
			out.Spec.Operations[i].Prerequisites = copyStrings(in.Spec.Operations[i].Prerequisites)
		}
	}
	if in.Spec.ExpiresAt != nil {
		out.Spec.ExpiresAt = in.Spec.ExpiresAt.DeepCopy()
	}
	out.Status.Outcomes = append([]OperationOutcome(nil), in.Status.Outcomes...)
	if in.Status.CompletedAt != nil {
		out.Status.CompletedAt = in.Status.CompletedAt.DeepCopy()
	}
	out.Status.Conditions = append([]metav1.Condition(nil), in.Status.Conditions...)
	return out
}

func (in *FortiGateDNSChangePlanList) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(FortiGateDNSChangePlanList)
	*out = *in
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]FortiGateDNSChangePlan, len(in.Items))
		for i := range in.Items {
			out.Items[i] = *(in.Items[i].DeepCopyObject().(*FortiGateDNSChangePlan))
		}
	}
	return out
}

func (in *FortiGateDNSStatus) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(FortiGateDNSStatus)
	*out = *in
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	if in.Status.LastAuditTime != nil {
		out.Status.LastAuditTime = in.Status.LastAuditTime.DeepCopy()
	}
	if in.Status.LastApplyTime != nil {
		out.Status.LastApplyTime = in.Status.LastApplyTime.DeepCopy()
	}
	out.Status.Conditions = append([]metav1.Condition(nil), in.Status.Conditions...)
	out.Status.History = append([]AuditSummary(nil), in.Status.History...)
	return out
}

func (in *FortiGateDNSStatusList) DeepCopyObject() runtime.Object {
	if in == nil {
		return nil
	}
	out := new(FortiGateDNSStatusList)
	*out = *in
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]FortiGateDNSStatus, len(in.Items))
		for i := range in.Items {
			out.Items[i] = *(in.Items[i].DeepCopyObject().(*FortiGateDNSStatus))
		}
	}
	return out
}
