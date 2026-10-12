package main

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayfake "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned/fake"

	v1alpha1 "github.com/kgskr/fortigate-external-dns/internal/apis/v1alpha1"
	"github.com/kgskr/fortigate-external-dns/internal/config"
	"github.com/kgskr/fortigate-external-dns/internal/controller"
	"github.com/kgskr/fortigate-external-dns/internal/metrics"
	"github.com/kgskr/fortigate-external-dns/internal/target"
	platformqueue "github.com/kgskr/fortigate-external-dns/internal/workqueue"
)

func TestTargetOnceOverridesEventMode(t *testing.T) {
	listFailure := errors.New("target list failed")
	for _, eventDriven := range []bool{false, true} {
		for _, failList := range []bool{false, true} {
			name := "polling"
			if eventDriven {
				name = "events"
			}
			if failList {
				name += " list failure"
			}
			t.Run(name, func(t *testing.T) {
				clients := integrationKubernetes(t, nil, nil)
				fakeDynamic := clients.Dynamic.(interface {
					PrependReactor(string, string, k8stesting.ReactionFunc)
					Actions() []k8stesting.Action
				})
				if failList {
					fakeDynamic.PrependReactor("list", "fortigatednstargets", func(k8stesting.Action) (bool, runtime.Object, error) {
						return true, nil, listFailure
					})
				}
				cfg := isolationConfig()
				cfg.Once, cfg.EventDriven = true, eventDriven
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				err := runTargetMode(ctx, cfg, clients, metrics.New(), discardLogger(), controller.NewHeartbeat())
				if ctx.Err() != nil {
					t.Fatal("one-shot target mode waited for cancellation instead of completing one cycle")
				}
				if failList && !errors.Is(err, listFailure) || !failList && err != nil {
					t.Fatalf("runTargetMode() = %v, list failure = %v", err, failList)
				}
				lists := 0
				for _, action := range fakeDynamic.Actions() {
					if action.GetVerb() == "watch" {
						t.Fatal("one-shot mode started an informer watch")
					}
					if action.GetVerb() == "list" && action.GetResource() == v1alpha1.TargetGVR {
						lists++
					}
				}
				if lists != 1 {
					t.Fatalf("target LIST count = %d, want exactly one cycle", lists)
				}
			})
		}
	}
}

func TestTargetEventSourcesMatchDiscoveryDefaults(t *testing.T) {
	tests := []struct {
		name        string
		definitions []target.Definition
		want        []string
	}{
		{name: "no targets"},
		{name: "omitted", definitions: []target.Definition{{}}, want: []string{"gateway", "ingress", "service"}},
		{name: "empty", definitions: []target.Definition{{Sources: []string{}}}, want: []string{"gateway", "ingress", "service"}},
		{name: "explicit service", definitions: []target.Definition{{Sources: []string{"service"}}}, want: []string{"service"}},
		{name: "explicit mixed", definitions: []target.Definition{{Sources: []string{"service"}}, {Sources: []string{"ingress", "gateway"}}}, want: []string{"gateway", "ingress", "service"}},
		{name: "explicit and omitted", definitions: []target.Definition{{Sources: []string{"service"}}, {}}, want: []string{"gateway", "ingress", "service"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Target sources have their own authority; a root service-only setting
			// must not narrow a target that defaults to every discovery source.
			scope := platformEventScope(config.Config{Sources: []string{"service"}}, tt.definitions)
			if !slices.Equal(scope.sources, tt.want) {
				t.Fatalf("informer sources = %v, want %v", scope.sources, tt.want)
			}
		})
	}
}

type modeEventExecutor struct {
	applied chan platformqueue.TargetKey
}

func (e *modeEventExecutor) Audit(context.Context, platformqueue.TargetKey) (controller.TargetAudit, error) {
	return controller.TargetAudit{}, nil
}

func (e *modeEventExecutor) Apply(ctx context.Context, key platformqueue.TargetKey, _ controller.TargetAudit) error {
	select {
	case e.applied <- key:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestOmittedTargetSourcesReceiveAllSourceEvents(t *testing.T) {
	object := crTarget(t, "edge", "example.com", nil, func(object *v1alpha1.FortiGateDNSTarget) {
		object.Spec.Sources = nil
	})
	clients := integrationKubernetes(t, nil, []runtime.Object{object})
	clients.Gateway = gatewayfake.NewSimpleClientset()
	var apiTarget v1alpha1.FortiGateDNSTarget
	if err := v1alpha1.FromUnstructured(object, &apiTarget); err != nil {
		t.Fatal(err)
	}
	scope := platformEventScope(isolationConfig(), []target.Definition{target.FromAPI(&apiTarget)})
	executor := &modeEventExecutor{applied: make(chan platformqueue.TargetKey, 16)}
	eventRuntime, err := controller.NewPlatformRuntime(
		controller.PlatformClients{Kubernetes: clients.Core, Gateway: clients.Gateway, Dynamic: clients.Dynamic},
		controller.PlatformRuntimeConfig{
			Namespace: "dns-system", Sources: scope.sources, PeriodicInterval: time.Hour,
			Queue: platformqueue.Config{DisableDebounce: true},
		}, executor,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- eventRuntime.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("event runtime shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("event runtime did not stop")
		}
	})
	waitForApply := func() {
		t.Helper()
		select {
		case key := <-executor.applied:
			if key.String() != "dns-system/edge" {
				t.Fatalf("unexpected target event: %s", key.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("source event did not trigger an audit before the periodic resync")
		}
	}
	waitForApply() // Initial audit occurs after every informer has synchronized.
	creates := []struct {
		name   string
		create func() error
	}{
		{name: "Service", create: func() error {
			_, err := clients.Core.CoreV1().Services("apps").Create(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "svc"}}, metav1.CreateOptions{})
			return err
		}},
		{name: "Ingress", create: func() error {
			_, err := clients.Core.NetworkingV1().Ingresses("apps").Create(ctx, &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "ingress"}}, metav1.CreateOptions{})
			return err
		}},
		{name: "Gateway", create: func() error {
			_, err := clients.Gateway.GatewayV1().Gateways("apps").Create(ctx, &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway"}}, metav1.CreateOptions{})
			return err
		}},
		{name: "HTTPRoute", create: func() error {
			_, err := clients.Gateway.GatewayV1().HTTPRoutes("apps").Create(ctx, &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "route"}}, metav1.CreateOptions{})
			return err
		}},
	}
	for _, step := range creates {
		if err := step.create(); err != nil {
			t.Fatalf("create %s: %v", step.name, err)
		}
		waitForApply()
	}
}
