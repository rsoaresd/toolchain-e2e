package operators

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/codeready-toolchain/toolchain-e2e/setup/configuration"
	"github.com/codeready-toolchain/toolchain-e2e/setup/test"
	"github.com/operator-framework/api/pkg/operators/v1alpha1"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestEnsureOperatorsInstalled(t *testing.T) {
	csvTimeout = time.Millisecond
	scheme, err := configuration.NewScheme()
	require.NoError(t, err)

	t.Run("success", func(t *testing.T) {
		t.Run("operator not installed", func(t *testing.T) {
			// given
			cl := test.NewFakeClient(t)
			cl.MockGet = func(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
				if sub, ok := obj.(*v1alpha1.Subscription); ok {
					sub.Status.CurrentCSV = "kiali-operator.v1.24.7" // set CurrentCSV to simulate a good subscription
					return nil
				}

				if csv, ok := obj.(*v1alpha1.ClusterServiceVersion); ok {
					kialiCSV := kialiCSV(v1alpha1.CSVPhaseSucceeded)
					kialiCSV.DeepCopyInto(csv)
					return nil
				}
				return cl.Client.Get(ctx, key, obj, opts...)
			}

			// when
			err = EnsureOperatorsInstalled(context.TODO(), cl, scheme, []string{"installtemplates/kiali.yaml"})

			// then
			require.NoError(t, err)
		})
	})

	t.Run("failures", func(t *testing.T) {
		configuration.DefaultTimeout = 1 * time.Millisecond
		configuration.DefaultRetryInterval = 1 * time.Millisecond

		t.Run("error when creating subscription", func(t *testing.T) {
			// given
			cl := test.NewFakeClient(t)
			cl.MockPatch = func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if obj.GetObjectKind().GroupVersionKind().Kind == "Subscription" {
					return fmt.Errorf("Test client error")
				}
				return cl.Client.Patch(ctx, obj, patch, opts...)
			}

			// when
			err := EnsureOperatorsInstalled(context.TODO(), cl, scheme, []string{"installtemplates/kiali.yaml"})

			// then
			require.EqualError(t, err, "could not apply resource 'kiali-ossm' in namespace 'openshift-operators': unable to patch 'operators.coreos.com/v1alpha1, Kind=Subscription' called 'kiali-ossm' in namespace 'openshift-operators': Test client error")
		})
		t.Run("error when getting subscription", func(t *testing.T) {
			// given
			cl := test.NewFakeClient(t)
			count := 0
			cl.MockGet = func(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
				if obj.GetObjectKind().GroupVersionKind().Kind == "Subscription" {
					if count > 1 { // ignore the first call because it's called from the applyclient
						return fmt.Errorf("Test client error")
					}
					count++
				}
				return cl.Client.Get(ctx, key, obj, opts...)
			}

			// when
			err := EnsureOperatorsInstalled(context.TODO(), cl, scheme, []string{"installtemplates/kiali.yaml"})

			// then
			require.ErrorContains(t, err, "could not find a Subscription with name 'kiali-ossm' in namespace 'openshift-operators' that meets the expected criteria: context deadline exceeded")
		})

		t.Run("error when getting csv", func(t *testing.T) {
			// given
			cl := test.NewFakeClient(t)
			cl.MockGet = func(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
				if sub, ok := obj.(*v1alpha1.Subscription); ok {
					sub.Status.CurrentCSV = "kiali-operator.v1.24.7" // set CurrentCSV to simulate a good subscription
					return nil
				}

				if obj.GetObjectKind().GroupVersionKind().Kind == "ClusterServiceVersion" {
					return fmt.Errorf("Test client error")
				}
				return cl.Client.Get(ctx, key, obj, opts...)
			}

			// when
			err := EnsureOperatorsInstalled(context.TODO(), cl, scheme, []string{"installtemplates/kiali.yaml"})

			// then
			require.EqualError(t, err, "failed to find CSV 'kiali-operator.v1.24.7' with Phase 'Succeeded': could not find a CSV with name 'kiali-operator.v1.24.7' in namespace 'openshift-operators' that meets the expected criteria: context deadline exceeded")
		})

		t.Run("csv has wrong phase", func(t *testing.T) {
			// given
			cl := test.NewFakeClient(t)
			cl.MockGet = func(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
				if sub, ok := obj.(*v1alpha1.Subscription); ok {
					sub.Status.CurrentCSV = "kiali-operator.v1.24.7" // set CurrentCSV to simulate a good subscription
					return nil
				}

				if csv, ok := obj.(*v1alpha1.ClusterServiceVersion); ok {
					kialiCSV := kialiCSV(v1alpha1.CSVPhaseFailed)
					kialiCSV.DeepCopyInto(csv)
					return nil
				}
				return cl.Client.Get(ctx, key, obj, opts...)
			}

			// when
			err = EnsureOperatorsInstalled(context.TODO(), cl, scheme, []string{"installtemplates/kiali.yaml"})

			// then
			require.EqualError(t, err, "failed to find CSV 'kiali-operator.v1.24.7' with Phase 'Succeeded': could not find a CSV with name 'kiali-operator.v1.24.7' in namespace 'openshift-operators' that meets the expected criteria: context deadline exceeded")
		})
		t.Run("no subscription in template", func(t *testing.T) {
			// given
			cl := test.NewFakeClient(t)

			// when
			err := EnsureOperatorsInstalled(context.TODO(), cl, scheme, []string{"../test/installtemplates/badoperator.yaml"})

			// then
			require.EqualError(t, err, "a subscription was not found in template file '../test/installtemplates/badoperator.yaml'")
		})
	})
}

func kialiCSV(phase v1alpha1.ClusterServiceVersionPhase) *v1alpha1.ClusterServiceVersion {
	return &v1alpha1.ClusterServiceVersion{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kiali-operator.v1.24.7",
			Namespace: "openshift-operators",
		},
		Spec: v1alpha1.ClusterServiceVersionSpec{},
		Status: v1alpha1.ClusterServiceVersionStatus{
			Phase: phase,
		},
	}
}
