package wait_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	toolchainv1alpha1 "github.com/codeready-toolchain/api/api/v1alpha1"
	testspace "github.com/codeready-toolchain/toolchain-common/pkg/test/space"
	"github.com/codeready-toolchain/toolchain-e2e/setup/configuration"
	"github.com/codeready-toolchain/toolchain-e2e/setup/test"
	"github.com/codeready-toolchain/toolchain-e2e/setup/wait"
	"github.com/operator-framework/api/pkg/operators/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestForSpace(t *testing.T) {
	configuration.DefaultTimeout = time.Millisecond * 1
	t.Run("success", func(t *testing.T) {
		// given
		space := testspace.NewSpace(configuration.HostOperatorNamespace, "user0001", testspace.WithCondition(
			toolchainv1alpha1.Condition{
				Type:   toolchainv1alpha1.ConditionReady,
				Status: corev1.ConditionTrue,
				Reason: "Provisioned",
			}))
		cl := test.NewFakeClient(t, space) // space exists

		// when
		err := wait.ForSpace(cl, "user0001")

		// then
		require.NoError(t, err)
	})

	t.Run("failures", func(t *testing.T) {
		t.Run("timeout", func(t *testing.T) {
			// given
			configuration.DefaultTimeout = time.Second * 1
			cl := test.NewFakeClient(t) // space doesn't exist

			// when
			err := wait.ForSpace(cl, "user0001")

			// then
			require.Error(t, err)
			assert.EqualError(t, err, "space 'user0001' is not ready yet: context deadline exceeded")
		})
	})
}

func TestHasSubscriptionWithCondition(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		t.Run("without criteria", func(t *testing.T) {
			// given
			sub := &v1alpha1.Subscription{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-prefix",
					Namespace: "test-ns",
				},
			}
			cl := test.NewFakeClient(t, sub) // subscription exists

			// when
			res, err := wait.HasSubscriptionWithCriteria(cl, "test-prefix", "test-ns")

			// then
			require.NoError(t, err)
			require.True(t, res)
		})

		t.Run("with matching criteria", func(t *testing.T) {
			// given
			sub := &v1alpha1.Subscription{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-prefix",
					Namespace: "test-ns",
				},
				Status: v1alpha1.SubscriptionStatus{
					CurrentCSV: "test-csv",
				},
			}
			cl := test.NewFakeClient(t, sub) // subscription exists

			// when
			res, err := wait.HasSubscriptionWithCriteria(cl, "test-prefix", "test-ns", func(sub *v1alpha1.Subscription) bool {
				return sub.Status.CurrentCSV == "test-csv"
			})

			// then
			require.NoError(t, err)
			require.True(t, res)
		})
	})

	t.Run("failures", func(t *testing.T) {
		t.Run("subscription does not exist", func(t *testing.T) {
			// given
			cl := test.NewFakeClient(t) // subscription does not exist

			// when
			res, err := wait.HasSubscriptionWithCriteria(cl, "test-prefix", "test-ns")

			// then
			require.NoError(t, err)
			require.False(t, res)
		})

		t.Run("client error", func(t *testing.T) {
			// given
			cl := test.NewFakeClient(t) // csv does not exist
			cl.MockGet = func(_ context.Context, _ types.NamespacedName, _ client.Object, _ ...client.GetOption) error {
				return fmt.Errorf("Test client error")
			}

			// when
			res, err := wait.HasSubscriptionWithCriteria(cl, "test-prefix", "test-ns")

			// then
			require.EqualError(t, err, "Test client error")
			require.False(t, res)
		})

		t.Run("no matching criteria", func(t *testing.T) {
			// given
			sub := &v1alpha1.Subscription{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-prefix",
					Namespace: "test-ns",
				},
				Status: v1alpha1.SubscriptionStatus{
					CurrentCSV: "test-csv",
				},
			}
			cl := test.NewFakeClient(t, sub) // subscription exists

			// when
			res, err := wait.HasSubscriptionWithCriteria(cl, "test-prefix", "test-ns", func(sub *v1alpha1.Subscription) bool {
				return sub.Status.CurrentCSV == "bad-csv"
			})

			// then
			require.NoError(t, err)
			require.False(t, res)
		})
	})
}

func TestForSubscriptionWithCriteria(t *testing.T) {
	configuration.DefaultTimeout = time.Millisecond * 1
	t.Run("success", func(t *testing.T) {
		// given
		sub := &v1alpha1.Subscription{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-prefix",
				Namespace: "test-ns",
			},
		}
		cl := test.NewFakeClient(t, sub) // subscription exists

		// when
		err := wait.ForSubscriptionWithCriteria(cl, "test-prefix", "test-ns", configuration.DefaultTimeout)

		// then
		require.NoError(t, err)
	})

	t.Run("failures", func(t *testing.T) {
		t.Run("subscription does not exist", func(t *testing.T) {
			// given
			cl := test.NewFakeClient(t) // subscription does not exist

			// when
			err := wait.ForSubscriptionWithCriteria(cl, "test-prefix", "test-ns", configuration.DefaultTimeout)

			// then
			require.EqualError(t, err, `could not find a Subscription with name 'test-prefix' in namespace 'test-ns' that meets the expected criteria: context deadline exceeded`)
		})

		t.Run("client error", func(t *testing.T) {
			// given
			cl := test.NewFakeClient(t)
			cl.MockGet = func(_ context.Context, _ types.NamespacedName, _ client.Object, _ ...client.GetOption) error {
				return fmt.Errorf("Test client error")
			}

			// when
			err := wait.ForSubscriptionWithCriteria(cl, "test-prefix", "test-ns", configuration.DefaultTimeout)

			// then
			require.EqualError(t, err, `could not find a Subscription with name 'test-prefix' in namespace 'test-ns' that meets the expected criteria: Test client error`)
		})
	})
}

func TestHasCSVWithPrefix(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		t.Run("without criteria", func(t *testing.T) {
			// given
			csv := &v1alpha1.ClusterServiceVersion{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-prefix",
					Namespace: "test-ns",
				},
			}
			cl := test.NewFakeClient(t, csv) // csv exists

			// when
			res, err := wait.HasCSVWithCriteria(cl, "test-prefix", "test-ns")

			// then
			require.NoError(t, err)
			require.True(t, res)
		})

		t.Run("with matching criteria", func(t *testing.T) {
			// given
			csv := &v1alpha1.ClusterServiceVersion{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-prefix",
					Namespace: "test-ns",
				},
				Status: v1alpha1.ClusterServiceVersionStatus{
					Message: "csv complete",
				},
			}
			cl := test.NewFakeClient(t, csv) // subscription exists

			// when
			res, err := wait.HasCSVWithCriteria(cl, "test-prefix", "test-ns", func(csv *v1alpha1.ClusterServiceVersion) bool {
				return csv.Status.Message == "csv complete"
			})

			// then
			require.NoError(t, err)
			require.True(t, res)
		})
	})

	t.Run("failures", func(t *testing.T) {
		t.Run("csv does not exist", func(t *testing.T) {
			// given
			cl := test.NewFakeClient(t) // csv does not exist

			// when
			res, err := wait.HasCSVWithCriteria(cl, "test-prefix", "test-ns")

			// then
			require.NoError(t, err)
			require.False(t, res)
		})

		t.Run("client error", func(t *testing.T) {
			// given
			cl := test.NewFakeClient(t) // csv does not exist
			cl.MockGet = func(_ context.Context, _ types.NamespacedName, _ client.Object, _ ...client.GetOption) error {
				return fmt.Errorf("Test client error")
			}

			// when
			res, err := wait.HasCSVWithCriteria(cl, "test-prefix", "test-ns")

			// then
			require.EqualError(t, err, "Test client error")
			require.False(t, res)
		})

		t.Run("with no matching criteria", func(t *testing.T) {
			// given
			csv := &v1alpha1.ClusterServiceVersion{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-prefix",
					Namespace: "test-ns",
				},
				Status: v1alpha1.ClusterServiceVersionStatus{
					Message: "csv complete",
				},
			}
			cl := test.NewFakeClient(t, csv) // csv exists

			// when
			res, err := wait.HasCSVWithCriteria(cl, "test-prefix", "test-ns", func(csv *v1alpha1.ClusterServiceVersion) bool {
				return csv.Status.Message == "test-csv"
			})

			// then
			require.NoError(t, err)
			require.False(t, res)
		})
	})
}

func TestForCSVWithCriteria(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		// given
		csv := &v1alpha1.ClusterServiceVersion{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-prefix",
				Namespace: "test-ns",
			},
		}
		cl := test.NewFakeClient(t, csv) // csv exists

		// when
		err := wait.ForCSVWithCriteria(cl, "test-prefix", "test-ns", time.Millisecond)

		// then
		require.NoError(t, err)
	})

	t.Run("failures", func(t *testing.T) {
		t.Run("csv does not exist", func(t *testing.T) {
			// given
			cl := test.NewFakeClient(t) // csv does not exist

			// when
			err := wait.ForCSVWithCriteria(cl, "test-prefix", "test-ns", time.Millisecond)

			// then
			require.EqualError(t, err, `could not find a CSV with name 'test-prefix' in namespace 'test-ns' that meets the expected criteria: context deadline exceeded`)
		})

		t.Run("client error", func(t *testing.T) {
			// given
			cl := test.NewFakeClient(t)
			cl.MockGet = func(_ context.Context, _ types.NamespacedName, _ client.Object, _ ...client.GetOption) error {
				return fmt.Errorf("Test client error")
			}

			// when
			err := wait.ForCSVWithCriteria(cl, "test-prefix", "test-ns", time.Millisecond)

			// then
			require.EqualError(t, err, `could not find a CSV with name 'test-prefix' in namespace 'test-ns' that meets the expected criteria: Test client error`)
		})
	})
}
