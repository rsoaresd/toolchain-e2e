package e2e

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	toolchainv1alpha1 "github.com/codeready-toolchain/api/api/v1alpha1"
	. "github.com/codeready-toolchain/toolchain-e2e/testsupport"
	"github.com/codeready-toolchain/toolchain-e2e/testsupport/kubeconfig"
	"github.com/codeready-toolchain/toolchain-e2e/testsupport/wait"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestToolchainClusterE2E(t *testing.T) {
	awaitilities := WaitForDeployments(t)
	hostAwait := awaitilities.Host()
	hostAwait.WaitForToolchainClusterResources(t)
	memberAwait := awaitilities.Member1()
	memberAwait.WaitForToolchainClusterResources(t)

	verifyToolchainCluster(t, hostAwait.Awaitility, memberAwait.Awaitility)
	verifyToolchainCluster(t, memberAwait.Awaitility, hostAwait.Awaitility)
}

// verifyToolchainCluster verifies existence and correct conditions of ToolchainCluster CRD
// in the target cluster type operator
func verifyToolchainCluster(t *testing.T, await *wait.Awaitility, otherAwait *wait.Awaitility) {
	// given
	current, ok, err := await.GetToolchainCluster(t, otherAwait.Namespace, toolchainv1alpha1.ConditionReady)
	require.NoError(t, err)
	require.True(t, ok, "ToolchainCluster should exist")

	// NOTE: this needs to run first, before the sub-tests below, because they reuse the secret for the new
	// toolchain clusters. This is technically incorrect but sufficient for those tests.
	// Note that we are going to be changing the workflow such that the label on the secret will actually be the driver
	// for ToolchainCluster creation and so re-using the secret for different TCs will become impossible in the future.
	t.Run("referenced secret is labeled", func(t *testing.T) {
		secret := corev1.Secret{}
		require.NoError(t, await.Client.Get(context.TODO(), client.ObjectKey{Name: current.Spec.SecretRef.Name, Namespace: current.Namespace}, &secret))

		require.Equal(t, current.Name, secret.Labels[toolchainv1alpha1.ToolchainClusterLabel], "the secret of the ToolchainCluster %s is not labeled", client.ObjectKeyFromObject(&current))
	})

	t.Run("status is populated with info from kubeconfig", func(t *testing.T) {
		secret := corev1.Secret{}
		require.NoError(t, await.Client.Get(context.TODO(), client.ObjectKey{Name: current.Spec.SecretRef.Name, Namespace: current.Namespace}, &secret))
		apiConfig, err := clientcmd.Load(secret.Data["kubeconfig"])
		require.NoError(t, err)

		configContext, present := apiConfig.Contexts[apiConfig.CurrentContext]
		require.True(t, present)

		operatorNamespace := configContext.Namespace
		apiEndpoint := apiConfig.Clusters[configContext.Cluster].Server

		assert.Equal(t, operatorNamespace, current.Status.OperatorNamespace)
		assert.Equal(t, apiEndpoint, current.Status.APIEndpoint)
	})

	t.Run(fmt.Sprintf("create new ToolchainCluster based on '%s' with correct data and expect to be ready", current.Name), func(t *testing.T) {
		// given
		name := generateNewName("new-ready-", current.Name)
		secretCopy := &corev1.Secret{}
		wait.CopyWithCleanup(t, await,
			client.ObjectKey{Name: current.Spec.SecretRef.Name, Namespace: current.Namespace},
			client.ObjectKey{Name: name, Namespace: current.Namespace},
			secretCopy)

		toolchainCluster := newToolchainCluster(await.Namespace, name,
			secretRef(secretCopy.Name),
			capacityExhausted, // make sure this cluster cannot be used in other e2e tests
		)

		// when
		err = await.CreateWithCleanup(t, toolchainCluster)
		require.NoError(t, err)
		// wait for toolchaincontroller to reconcile
		time.Sleep(1 * time.Second)

		// then the ToolchainCluster should be ready
		_, err = await.WaitForToolchainCluster(t,
			wait.UntilToolchainClusterHasName(toolchainCluster.Name),
			wait.UntilToolchainClusterHasCondition(toolchainv1alpha1.ConditionReady),
		)
		require.NoError(t, err)

		// other ToolchainCluster should be ready, too
		_, err = await.WaitForToolchainCluster(t,
			wait.UntilToolchainClusterHasOperatorNamespace(otherAwait.Namespace), wait.UntilToolchainClusterHasCondition(toolchainv1alpha1.ConditionReady),
		)
		require.NoError(t, err)

		_, err = otherAwait.WaitForToolchainCluster(t,
			wait.UntilToolchainClusterHasOperatorNamespace(await.Namespace), wait.UntilToolchainClusterHasCondition(toolchainv1alpha1.ConditionReady),
		)
		require.NoError(t, err)
	})

	t.Run(fmt.Sprintf("create new ToolchainCluster based on '%s' with incorrect data and expect to be Not Ready", current.Name), func(t *testing.T) {
		// given
		name := generateNewName("new-offline-", current.Name)
		secretCopy := &corev1.Secret{}
		wait.CopyWithCleanup(t, await, client.ObjectKey{Name: current.Spec.SecretRef.Name, Namespace: current.Namespace},
			client.ObjectKey{Name: name, Namespace: current.Namespace}, secretCopy, kubeconfig.Modify(t, kubeconfig.ApiEndpoint("https://1.2.3.4:8443")))

		toolchainCluster := newToolchainCluster(await.Namespace, name,
			secretRef(secretCopy.Name),
			capacityExhausted, // make sure this cluster cannot be used in other e2e tests
		)

		// when
		err := await.CreateWithCleanup(t, toolchainCluster)
		// wait for toolchaincontroller to reconcile
		time.Sleep(1 * time.Second)

		// then the ToolchainCluster should be Not Ready
		require.NoError(t, err)

		_, err = await.WaitForToolchainCluster(t,
			wait.UntilToolchainClusterHasName(toolchainCluster.Name),
			wait.UntilToolchainClusterHasConditionFalseStatusAndReason(toolchainv1alpha1.ConditionReady, toolchainv1alpha1.ToolchainClusterClusterNotReachableReason),
		)
		require.NoError(t, err)

		// other ToolchainCluster should be ready, too
		_, err = await.WaitForToolchainCluster(t,
			wait.UntilToolchainClusterHasOperatorNamespace(otherAwait.Namespace), wait.UntilToolchainClusterHasCondition(toolchainv1alpha1.ConditionReady),
		)
		require.NoError(t, err)

		_, err = otherAwait.WaitForToolchainCluster(t,
			wait.UntilToolchainClusterHasOperatorNamespace(await.Namespace), wait.UntilToolchainClusterHasCondition(toolchainv1alpha1.ConditionReady),
		)
		require.NoError(t, err)
	})
}

func generateNewName(prefix, baseName string) string {
	name := prefix + baseName
	if len(name) > 63 {
		return name[:63]
	}
	return name
}

func newToolchainCluster(namespace, name string, options ...clusterOption) *toolchainv1alpha1.ToolchainCluster {
	toolchainCluster := &toolchainv1alpha1.ToolchainCluster{
		Spec: toolchainv1alpha1.ToolchainClusterSpec{
			SecretRef: toolchainv1alpha1.LocalSecretReference{
				Name: "", // default
			},
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels: map[string]string{
				"ownerClusterName": "east",
			},
		},
	}
	for _, configure := range options {
		configure(toolchainCluster)
	}
	return toolchainCluster
}

// clusterOption an option to configure the cluster to use in the tests
type clusterOption func(*toolchainv1alpha1.ToolchainCluster)

// capacityExhausted an option to state that the cluster capacity has exhausted
var capacityExhausted clusterOption = func(c *toolchainv1alpha1.ToolchainCluster) {
	c.Labels["toolchain.dev.openshift.com/capacity-exhausted"] = strconv.FormatBool(true)
}

// SecretRef sets the SecretRef in the cluster's Spec
func secretRef(ref string) clusterOption {
	return func(c *toolchainv1alpha1.ToolchainCluster) {
		c.Spec.SecretRef = toolchainv1alpha1.LocalSecretReference{
			Name: ref,
		}
	}
}
