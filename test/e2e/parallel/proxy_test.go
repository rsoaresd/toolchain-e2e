package parallel

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"

	toolchainv1alpha1 "github.com/codeready-toolchain/api/api/v1alpha1"
	commonproxy "github.com/codeready-toolchain/toolchain-common/pkg/proxy"
	testspace "github.com/codeready-toolchain/toolchain-common/pkg/test/space"
	spacebindingrequesttestcommon "github.com/codeready-toolchain/toolchain-common/pkg/test/spacebindingrequest"
	. "github.com/codeready-toolchain/toolchain-e2e/testsupport"
	appstudiov1 "github.com/codeready-toolchain/toolchain-e2e/testsupport/appstudio/api/v1alpha1"
	testsupportspace "github.com/codeready-toolchain/toolchain-e2e/testsupport/space"
	. "github.com/codeready-toolchain/toolchain-e2e/testsupport/spacebinding"
	"github.com/codeready-toolchain/toolchain-e2e/testsupport/tiers"
	"github.com/codeready-toolchain/toolchain-e2e/testsupport/wait"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubewait "k8s.io/apimachinery/pkg/util/wait"
)

type proxyUser struct {
	expectedMemberCluster *wait.MemberAwaitility
	username              string
	token                 string
	identityID            uuid.UUID
	signup                *toolchainv1alpha1.UserSignup
	compliantUsername     string
}

type patchStringValue struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value string `json:"value"`
}

func (u *proxyUser) shareSpaceWith(t *testing.T, awaitilities wait.Awaitilities, guestUser *proxyUser) *toolchainv1alpha1.SpaceBindingRequest {
	// share primaryUser space with guestUser
	guestUserMur, err := awaitilities.Host().GetMasterUserRecord(guestUser.compliantUsername)
	require.NoError(t, err)
	primaryUserSpace, err := awaitilities.Host().WaitForSpace(t, u.compliantUsername, wait.UntilSpaceHasAnyTargetClusterSet(), wait.UntilSpaceHasAnyTierNameSet(), wait.UntilSpaceHasAnyProvisionedNamespaces())
	require.NoError(t, err)
	spaceBindingRequest := CreateSpaceBindingRequest(t, awaitilities, primaryUserSpace.Spec.TargetCluster,
		WithSpecSpaceRole("admin"),
		WithSpecMasterUserRecord(guestUserMur.GetName()),
		WithNamespace(testsupportspace.GetDefaultNamespace(primaryUserSpace.Status.ProvisionedNamespaces)),
	)
	_, err = awaitilities.Host().WaitForSpaceBinding(t, guestUserMur.GetName(), primaryUserSpace.GetName())
	require.NoError(t, err)
	return spaceBindingRequest
}

func (u *proxyUser) invalidShareSpaceWith(t *testing.T, awaitilities wait.Awaitilities, guestUser *proxyUser) *toolchainv1alpha1.SpaceBindingRequest {
	// share primaryUser space with guestUser
	guestUserMur, err := awaitilities.Host().GetMasterUserRecord(guestUser.compliantUsername)
	require.NoError(t, err)
	primaryUserSpace, err := awaitilities.Host().WaitForSpace(t, u.compliantUsername, wait.UntilSpaceHasAnyTargetClusterSet(), wait.UntilSpaceHasAnyTierNameSet(), wait.UntilSpaceHasAnyProvisionedNamespaces())
	require.NoError(t, err)
	spaceBindingRequest := CreateSpaceBindingRequest(t, awaitilities, primaryUserSpace.Spec.TargetCluster,
		WithSpecSpaceRole("invalidRole"),
		WithSpecMasterUserRecord(guestUserMur.GetName()),
		WithNamespace(testsupportspace.GetDefaultNamespace(primaryUserSpace.Status.ProvisionedNamespaces)),
	)
	// wait for spacebinding request status to be set
	_, err = awaitilities.Member1().WaitForSpaceBindingRequest(t, types.NamespacedName{Namespace: spaceBindingRequest.GetNamespace(), Name: spaceBindingRequest.GetName()},
		wait.UntilSpaceBindingRequestHasConditions(spacebindingrequesttestcommon.UnableToCreateSpaceBinding(fmt.Sprintf("invalid role 'invalidRole' for space '%s'", primaryUserSpace.Name))),
	)
	require.NoError(t, err)
	return spaceBindingRequest
}

func (u *proxyUser) listWorkspaces(t *testing.T, hostAwait *wait.HostAwaitility) []toolchainv1alpha1.Workspace {
	proxyCl := u.createProxyClient(t, hostAwait)

	workspaces := &toolchainv1alpha1.WorkspaceList{}
	err := proxyCl.List(context.TODO(), workspaces)
	require.NoError(t, err)
	return workspaces.Items
}

func (u *proxyUser) createProxyClient(t *testing.T, hostAwait *wait.HostAwaitility) client.Client {
	proxyCl, err := hostAwait.CreateAPIProxyClient(t, u.token, hostAwait.APIProxyURL)
	require.NoError(t, err)
	return proxyCl
}

func (u *proxyUser) getWorkspace(t *testing.T, hostAwait *wait.HostAwaitility, workspaceName string) (*toolchainv1alpha1.Workspace, error) {
	proxyCl := u.createProxyClient(t, hostAwait)

	workspace := &toolchainv1alpha1.Workspace{}
	var cause error
	// only wait up to 5 seconds because in some test cases the workspace is not expected to be found
	_ = kubewait.PollUntilContextTimeout(context.TODO(), wait.DefaultRetryInterval, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		cause = proxyCl.Get(context.TODO(), types.NamespacedName{Name: workspaceName}, workspace)
		return cause == nil, nil
	})

	// do not assert error before returning because in some test cases the workspace is not expected to be found
	return workspace, cause
}

func (u *proxyUser) getApplication(t *testing.T, proxyClient client.Client, applicationName string) *appstudiov1.Application {
	app := &appstudiov1.Application{}
	namespacedName := types.NamespacedName{Namespace: tenantNsName(u.compliantUsername), Name: applicationName}
	// Get Application
	err := proxyClient.Get(context.TODO(), namespacedName, app)
	require.NoError(t, err)
	require.NotEmpty(t, app)
	return app
}

func (u *proxyUser) getApplicationWithoutProxy(t *testing.T, applicationName string) *appstudiov1.Application {
	namespacedName := types.NamespacedName{Namespace: tenantNsName(u.compliantUsername), Name: applicationName}
	app := &appstudiov1.Application{}
	err := u.expectedMemberCluster.Client.Get(context.TODO(), namespacedName, app)
	require.NoError(t, err)
	require.NotEmpty(t, app)
	return app
}

func (u *proxyUser) getApplicationName(i int) string {
	return fmt.Sprintf("%s-test-app-%d", u.compliantUsername, i)
}

// full flow from usersignup with approval down to namespaces creation and cleanup
//
// !!! Additional context !!!
// The test uses a dummy HAS API type called Application. The reason is that the regular
// user doesn't have full permission for the standard types like ConfigMap. This means
// that we could do create/read operations on that resource from this test.
// To work around this limitation, we created a dummy HAS API type that has the same name
// and the same group as the actual one. The CRD is created as part of the test setup
// and since the CRD name & group name matches, then RBAC allow us to execute create/read
// operations on that resource using the user permissions.
func TestProxyFlow(t *testing.T) {
	t.Parallel()
	// given
	awaitilities := WaitForDeployments(t)
	hostAwait := awaitilities.Host()
	memberAwait := awaitilities.Member1()
	memberAwait2 := awaitilities.Member2()

	t.Logf("Proxy URL: %s", hostAwait.APIProxyURL)

	waitForWatcher := runWatcher(t, awaitilities)
	defer func() {
		t.Log("wait until the watcher is stopped")
		waitForWatcher.Wait()
	}()

	users := []*proxyUser{
		{
			expectedMemberCluster: memberAwait,
			username:              "proxymember1",
			identityID:            uuid.New(),
		},
		{
			expectedMemberCluster: memberAwait2,
			username:              "proxymember2",
			identityID:            uuid.New(),
		},
		{
			expectedMemberCluster: memberAwait,
			username:              "compliant.username", // contains a '.' that is valid in the username but should not be in the impersonation header since it should use the compliant username
			identityID:            uuid.New(),
		},
	}
	//create the users before the subtests, so they exist for the duration of the whole "ProxyFlow" test ;)
	for _, user := range users {
		createAppStudioUser(t, awaitilities, user)
	}

	for index, user := range users {
		t.Run(user.username, func(t *testing.T) {
			// Start a new websocket watcher
			w := newWsWatcher(t, *user, user.compliantUsername, hostAwait.APIProxyURL)
			closeConnection := w.Start()
			defer closeConnection()
			proxyCl := user.createProxyClient(t, hostAwait)
			applicationList := &appstudiov1.ApplicationList{}

			t.Run("use proxy to create a HAS Application CR in the user appstudio namespace via proxy API and use websocket to watch it created", func(t *testing.T) {
				// Create and retrieve the application resources multiple times for the same user to make sure the proxy cache kicks in.
				for i := 0; i < 2; i++ {
					// given
					applicationName := user.getApplicationName(i)
					expectedApp := newApplication(applicationName, tenantNsName(user.compliantUsername))

					// when
					err := proxyCl.Create(context.TODO(), expectedApp)
					require.NoError(t, err)

					// then
					// wait for the websocket watcher which uses the proxy to receive the Application CR
					found, err := w.WaitForApplication(
						expectedApp.Name,
					)
					require.NoError(t, err)
					assert.NotEmpty(t, found)
					assert.Equal(t, expectedApp.Spec, found.Spec)

					proxyApp := user.getApplication(t, proxyCl, applicationName)
					assert.NotEmpty(t, proxyApp)

					// Double check that the Application does exist using a regular client (non-proxy)
					noProxyApp := user.getApplicationWithoutProxy(t, applicationName)
					assert.Equal(t, expectedApp.Spec, noProxyApp.Spec)
				}

				t.Run("use proxy to update a HAS Application CR in the user appstudio namespace via proxy API", func(t *testing.T) {
					// Update application
					applicationName := user.getApplicationName(0)
					// Get application
					proxyApp := user.getApplication(t, proxyCl, applicationName)
					// Update DisplayName
					changedDisplayName := fmt.Sprintf("Proxy test for user %s - updated application", tenantNsName(user.compliantUsername))
					proxyApp.Spec.DisplayName = changedDisplayName
					err := proxyCl.Update(context.TODO(), proxyApp)
					require.NoError(t, err)

					// Find application and check, if it is updated
					updatedApp := user.getApplication(t, proxyCl, applicationName)
					assert.Equal(t, proxyApp.Spec.DisplayName, updatedApp.Spec.DisplayName)

					// Check that the Application is updated using a regular client (non-proxy)
					noProxyUpdatedApp := user.getApplicationWithoutProxy(t, applicationName)
					assert.Equal(t, proxyApp.Spec.DisplayName, noProxyUpdatedApp.Spec.DisplayName)
				})

				t.Run("use proxy to list a HAS Application CR in the user appstudio namespace", func(t *testing.T) {
					// Get List of applications.
					err := proxyCl.List(context.TODO(), applicationList, &client.ListOptions{Namespace: tenantNsName(user.compliantUsername)})
					// User should be able to list applications
					require.NoError(t, err)
					assert.NotEmpty(t, applicationList.Items)

					// Check that the applicationList using a regular client (non-proxy)
					applicationListWS := &appstudiov1.ApplicationList{}
					err = user.expectedMemberCluster.Client.List(context.TODO(), applicationListWS, &client.ListOptions{Namespace: tenantNsName(user.compliantUsername)})
					require.NoError(t, err)
					require.Len(t, applicationListWS.Items, 2)
					assert.Equal(t, applicationListWS.Items, applicationList.Items)
				})

				t.Run("use proxy to patch a HAS Application CR in the user appstudio namespace via proxy API", func(t *testing.T) {
					// Patch application
					applicationName := user.getApplicationName(1)
					patchString := "Patched application for proxy test"
					// Get application
					proxyApp := user.getApplication(t, proxyCl, applicationName)
					// Patch for DisplayName
					patchPayload := []patchStringValue{{
						Op:    "replace",
						Path:  "/spec/displayName",
						Value: patchString,
					}}
					patchPayloadBytes, err := json.Marshal(patchPayload)
					require.NoError(t, err)

					// Appply Patch
					err = proxyCl.Patch(context.TODO(), proxyApp, client.RawPatch(types.JSONPatchType, patchPayloadBytes))
					require.NoError(t, err)

					// Get patched app and verify patched DisplayName
					patchedApp := user.getApplication(t, proxyCl, applicationName)
					assert.Equal(t, patchString, patchedApp.Spec.DisplayName)

					// Double check that the Application is patched using a regular client (non-proxy)
					noProxyApp := user.getApplicationWithoutProxy(t, applicationName)
					assert.Equal(t, patchString, noProxyApp.Spec.DisplayName)
				})

				t.Run("use proxy to delete a HAS Application CR in the user appstudio namespace via proxy API and use websocket to watch it deleted", func(t *testing.T) {
					// Delete applications
					for i := 0; i < len(applicationList.Items); i++ {
						// Get application
						proxyApp := applicationList.Items[i].DeepCopy()
						// Delete
						err := proxyCl.Delete(context.TODO(), proxyApp)
						require.NoError(t, err)
						err = w.WaitForApplicationDeletion(
							proxyApp.Name,
						)
						require.NoError(t, err)

						// Check that the Application is deleted using a regular client (non-proxy)
						namespacedName := types.NamespacedName{Namespace: tenantNsName(user.compliantUsername), Name: proxyApp.Name}
						originalApp := &appstudiov1.Application{}
						err = user.expectedMemberCluster.Client.Get(context.TODO(), namespacedName, originalApp)
						require.Error(t, err) //not found
						require.True(t, k8serr.IsNotFound(err))
					}
				})
			})

			t.Run("get resource from namespace outside of user's workspace", func(t *testing.T) {
				// given

				// create a namespace which does not belong to the user's workspace
				anotherNamespace := fmt.Sprintf("outside-%s", user.compliantUsername)
				user.expectedMemberCluster.CreateNamespace(t, anotherNamespace)

				cm := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "dummy-cm",
						Namespace: anotherNamespace,
					},
					Data: map[string]string{
						"dummy": "dummy",
					},
				}
				err := user.expectedMemberCluster.Create(t, cm)
				require.NoError(t, err)
				_, err = memberAwait.WaitForConfigMap(t, anotherNamespace, "dummy-cm")
				require.NoError(t, err)

				proxyWorkspaceURL := hostAwait.ProxyURLWithWorkspaceContext(user.compliantUsername)
				explicitWorkspaceCtxClient, err := hostAwait.CreateAPIProxyClient(t, user.token, proxyWorkspaceURL) // a client with a workspace context
				require.NoError(t, err)
				withoutWorkspaceCtxClient := user.createProxyClient(t, hostAwait) // a client with no workspace context

				t.Run("fail when accessing unshared namespace", func(t *testing.T) {
					cm := corev1.ConfigMap{}
					t.Run("using explicit workspace context", func(t *testing.T) {
						// when
						// the namespace has a CM "dummy-cm" but we can't get it because the user doesn't have access to the namespace
						err = explicitWorkspaceCtxClient.Get(context.TODO(), types.NamespacedName{Namespace: anotherNamespace, Name: "dummy-cm"}, &cm)
						// then
						require.EqualError(t, err, fmt.Sprintf(`configmaps "dummy-cm" is forbidden: User "%s" cannot get resource "configmaps" in API group "" in the namespace "%s"`, user.compliantUsername, anotherNamespace))
					})
					t.Run("not using any workspace context", func(t *testing.T) {
						// when
						// the namespace has a CM "dummy-cm" but we can't get it because the user doesn't have access to the namespace
						err := withoutWorkspaceCtxClient.Get(context.TODO(), types.NamespacedName{Namespace: anotherNamespace, Name: "dummy-cm"}, &cm)
						// then
						require.EqualError(t, err, fmt.Sprintf(`configmaps "dummy-cm" is forbidden: User "%s" cannot get resource "configmaps" in API group "" in the namespace "%s"`, user.compliantUsername, anotherNamespace))
					})
				})

				t.Run("success when accessing shared namespace", func(t *testing.T) {
					// when
					// grand all authenticated users view access to the namespace
					rb := rbacv1.RoleBinding{
						TypeMeta: metav1.TypeMeta{
							Kind: "RoleBinding",
						},
						ObjectMeta: metav1.ObjectMeta{
							Name:      "authenticated-view",
							Namespace: anotherNamespace,
						},
						RoleRef: rbacv1.RoleRef{
							APIGroup: "rbac.authorization.k8s.io",
							Kind:     "ClusterRole",
							Name:     "view",
						},
						Subjects: []rbacv1.Subject{
							{
								Kind:     "Group",
								APIGroup: "rbac.authorization.k8s.io",
								Name:     "system:authenticated",
							},
						},
					}
					err := user.expectedMemberCluster.Create(t, &rb)
					require.NoError(t, err)
					_, err = user.expectedMemberCluster.WaitForRoleBinding(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: rb.Namespace}}, rb.Name)
					require.NoError(t, err)

					t.Run("using explicit workspace context", func(t *testing.T) {
						// when
						cm := corev1.ConfigMap{}
						// the namespace has a CM "dummy-cm", let's get it and check that it's actually from the shared namespace
						err = explicitWorkspaceCtxClient.Get(context.TODO(), types.NamespacedName{Namespace: anotherNamespace, Name: "dummy-cm"}, &cm)
						// then
						require.NoError(t, err)
						assert.Equal(t, anotherNamespace, cm.Namespace)
					})
					t.Run("not using any workspace context", func(t *testing.T) {
						// when
						cm := corev1.ConfigMap{}
						// the namespace has a CM "dummy-cm", let's get it and check that it's actually from the shared namespace
						err := withoutWorkspaceCtxClient.Get(context.TODO(), types.NamespacedName{Namespace: anotherNamespace, Name: "dummy-cm"}, &cm)
						// then
						require.NoError(t, err)
						assert.Equal(t, anotherNamespace, cm.Namespace)
					})
				})
			})

			t.Run("unable to create a resource in the other users namespace because the workspace is not shared", func(t *testing.T) {
				// given
				otherUser := users[(index+1)%len(users)]
				t.Log("other user: ", otherUser.username)
				// verify other user's namespace still exists
				ns := &corev1.Namespace{}
				namespaceName := tenantNsName(otherUser.compliantUsername)
				err := hostAwait.Client.Get(context.TODO(), types.NamespacedName{Name: namespaceName}, ns)
				require.NoError(t, err, "the other user's namespace should still exist")

				// when
				appName := fmt.Sprintf("%s-proxy-test-app", user.compliantUsername)
				appToCreate := &appstudiov1.Application{
					ObjectMeta: metav1.ObjectMeta{
						Name:      appName,
						Namespace: namespaceName, // user should not be allowed to create a resource in the other user's namespace
					},
					Spec: appstudiov1.ApplicationSpec{
						DisplayName: "Should be forbidden",
					},
				}
				workspaceName := otherUser.compliantUsername
				proxyWorkspaceURL := hostAwait.ProxyURLWithWorkspaceContext(workspaceName) // set workspace context to other user's workspace
				proxyCl, err := hostAwait.CreateAPIProxyClient(t, user.token, proxyWorkspaceURL)
				require.NoError(t, err)
				err = proxyCl.Create(context.TODO(), appToCreate)
				// Sep 26 2024
				// As mentioned in the issue -https://github.com/kubernetes-sigs/controller-runtime/issues/2354 introduced in controller-runtime v0.15
				// for this test, instead of expecting the NoMatchError, discovery client fails and returns a server list error like 'failed to get API group resources: unable to retrieve the complete list of server APIs'
				// The returned server error wraps around the actual error message of ""invalid workspace request", thus check error contains instead of error equals
				// TO-DO : This issue is fixed in controller-runtime v0.17 - revisit it then
				require.ErrorContains(t, err, fmt.Sprintf("invalid workspace request: access to workspace '%s' is forbidden", workspaceName))
			})

			t.Run("successful workspace context request", func(t *testing.T) {
				proxyWorkspaceURL := hostAwait.ProxyURLWithWorkspaceContext(user.compliantUsername)
				// Start a new websocket watcher which watches for Application CRs in the user's namespace
				w := newWsWatcher(t, *user, user.compliantUsername, proxyWorkspaceURL)
				closeConnection := w.Start()
				defer closeConnection()
				workspaceCl, err := hostAwait.CreateAPIProxyClient(t, user.token, proxyWorkspaceURL) // proxy client with workspace context
				require.NoError(t, err)

				// given
				applicationName := fmt.Sprintf("%s-workspace-context", user.compliantUsername)
				namespaceName := tenantNsName(user.compliantUsername)
				expectedApp := newApplication(applicationName, namespaceName)

				// when
				err = workspaceCl.Create(context.TODO(), expectedApp)
				require.NoError(t, err)

				// then
				// wait for the websocket watcher which uses the proxy to receive the Application CR
				found, err := w.WaitForApplication(
					expectedApp.Name,
				)
				require.NoError(t, err)
				assert.NotEmpty(t, found)

				// Double check that the Application does exist using a regular client (non-proxy)
				createdApp := &appstudiov1.Application{}
				err = user.expectedMemberCluster.Client.Get(context.TODO(), types.NamespacedName{Namespace: namespaceName, Name: applicationName}, createdApp)
				require.NoError(t, err)
				require.NotEmpty(t, createdApp)
				assert.Equal(t, expectedApp.Spec.DisplayName, createdApp.Spec.DisplayName)
			}) // end of successful workspace context request

			t.Run("successful workspace context request with proxy plugin", func(t *testing.T) {
				// we are going to repurpose a well known, always running route as a proxy plugin to contact through the registration service
				CreateProxyPluginWithCleanup(t, hostAwait, "openshift-console", "openshift-console", "console")
				VerifyProxyPlugin(t, hostAwait, "openshift-console")
				proxyPluginWorkspaceURL := hostAwait.PluginProxyURLWithWorkspaceContext("openshift-console", user.compliantUsername)
				client := http.Client{
					Timeout: 30 * time.Second,
					Transport: &http.Transport{
						TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
					},
				}
				request, err := http.NewRequest("GET", proxyPluginWorkspaceURL, nil)
				require.NoError(t, err)

				request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", user.token))
				var resp *http.Response
				resp, err = client.Do(request)
				require.NoError(t, err)
				defer resp.Body.Close()
				var body []byte
				body, err = io.ReadAll(resp.Body)
				require.NoError(t, err)
				bodyStr := string(body)
				if resp.StatusCode != http.StatusOK {
					t.Errorf("unexpected http return code of %d with body text %s", resp.StatusCode, bodyStr)
				}
				if !strings.Contains(bodyStr, "Red") || !strings.Contains(bodyStr, "Open") {
					t.Errorf("unexpected http response body %s", bodyStr)
				}
			}) // end of successful workspace context request with proxy plugin

			t.Run("invalid workspace context request", func(t *testing.T) {
				// given
				proxyWorkspaceURL := hostAwait.ProxyURLWithWorkspaceContext("notexist")
				hostAwaitWithShorterTimeout := hostAwait.WithRetryOptions(wait.TimeoutOption(time.Second * 3)) // we expect an error so we can use a shorter timeout
				proxyCl, err := hostAwaitWithShorterTimeout.CreateAPIProxyClient(t, user.token, proxyWorkspaceURL)
				require.NoError(t, err)
				expectedApp := &appstudiov1.Application{}
				// when
				err = proxyCl.Get(context.TODO(), types.NamespacedName{Namespace: "testNamespace", Name: "test"}, expectedApp)
				// then
				require.ErrorContains(t, err, `an error on the server ("unable to get target cluster: access to workspace 'notexist' is forbidden") has prevented the request from succeeding`)
			})

			t.Run("invalid request headers", func(t *testing.T) {
				// given
				proxyWorkspaceURL := hostAwait.ProxyURLWithWorkspaceContext(user.compliantUsername)
				rejectedHeaders := []headerKeyValue{
					{"Impersonate-Group", "system:cluster-admins"},
					{"Impersonate-Group", "system:node-admins"},
				}
				client := http.Client{
					Timeout: time.Duration(5 * time.Second), // because sometimes the network connection may be a bit slow
				}
				client.Transport = &http.Transport{
					TLSClientConfig: &tls.Config{
						InsecureSkipVerify: true, // nolint:gosec
					},
				}
				t.Logf("proxyWorkspaceURL: %s", proxyWorkspaceURL)
				nodesURL := fmt.Sprintf("%s/api/v1/nodes", proxyWorkspaceURL)
				t.Logf("nodesURL: %s", nodesURL)

				for _, header := range rejectedHeaders {
					t.Run(fmt.Sprintf("k=%s,v=%s", header.key, header.value), func(t *testing.T) {
						// given
						request, err := http.NewRequest("GET", nodesURL, nil)
						request.Header.Add(header.key, header.value)
						require.NoError(t, err)
						request.Header.Add("Authorization", fmt.Sprintf("Bearer %s", user.token)) // uses the user's token with the impersonation headers

						// when
						resp, err := client.Do(request)

						// then
						require.NoError(t, err)
						require.NotNil(t, resp)
						defer resp.Body.Close()
						require.Equal(t, 403, resp.StatusCode) // should be forbidden
						r, _ := io.ReadAll(resp.Body)
						assert.Contains(t, string(r), fmt.Sprintf(`nodes is forbidden: User \"%s\" cannot list resource \"nodes\" in API group \"\" at the cluster scope`, user.compliantUsername))
					})
				}
			}) // end of invalid request headers
		})
	} // end users loop

	t.Run("proxy with shared workspace use cases", func(t *testing.T) {
		// given
		guestUser := users[0]
		primaryUser := users[1]
		applicationName := fmt.Sprintf("%s-share-workspace-context", primaryUser.compliantUsername)
		workspaceName := primaryUser.compliantUsername
		primaryUserWorkspaceURL := hostAwait.ProxyURLWithWorkspaceContext(workspaceName) // set workspace context using primaryUser's workspace

		// ensure the app exists in primaryUser's space
		primaryUserNamespace := tenantNsName(primaryUser.compliantUsername)
		expectedApp := newApplication(applicationName, primaryUserNamespace)
		err := primaryUser.expectedMemberCluster.Client.Create(context.TODO(), expectedApp)
		require.NoError(t, err)

		t.Run("guestUser request to unauthorized workspace", func(t *testing.T) {
			proxyCl, err := hostAwait.CreateAPIProxyClient(t, guestUser.token, primaryUserWorkspaceURL)
			require.NoError(t, err)

			// when
			actualApp := &appstudiov1.Application{}
			err = proxyCl.Get(context.TODO(), types.NamespacedName{Name: applicationName, Namespace: primaryUserNamespace}, actualApp)

			// then
			// Sep 26 2024
			// As mentioned in the issue -https://github.com/kubernetes-sigs/controller-runtime/issues/2354 introduced in controller-runtime v0.15
			// for this test, instead of expecting the NoMatchError, discovery client fails and returns a server list error like 'failed to get API group resources: unable to retrieve the complete list of server APIs'
			// The returned server error wraps around the actual error message of ""invalid workspace request", thus check error contains instead of error equals
			// TO-DO : This issue is fixed in controller-runtime v0.17 - revisit it then
			require.ErrorContains(t, err, "invalid workspace request: access to workspace 'proxymember2' is forbidden")

			// Double check that the Application does exist using a regular client (non-proxy)
			createdApp := &appstudiov1.Application{}
			err = guestUser.expectedMemberCluster.Client.Get(context.TODO(), types.NamespacedName{Namespace: primaryUserNamespace, Name: applicationName}, createdApp)
			require.NoError(t, err)
			require.NotEmpty(t, createdApp)
			assert.Equal(t, expectedApp.Spec.DisplayName, createdApp.Spec.DisplayName)
		})

		t.Run("share primaryUser workspace with guestUser", func(t *testing.T) {
			// given

			// share primaryUser space with guestUser
			primaryUser.shareSpaceWith(t, awaitilities, guestUser)

			// VerifySpaceRelatedResources will verify the roles and rolebindings are updated to include guestUser's SpaceBinding
			VerifySpaceRelatedResources(t, awaitilities, primaryUser.signup, "appstudio")

			// Start a new websocket watcher which watches for Application CRs in the user's namespace
			w := newWsWatcher(t, *guestUser, primaryUser.compliantUsername, primaryUserWorkspaceURL)
			closeConnection := w.Start()
			defer closeConnection()
			guestUserPrimaryWsCl, err := hostAwait.CreateAPIProxyClient(t, guestUser.token, primaryUserWorkspaceURL)
			require.NoError(t, err)

			// when
			// user A requests the Application CR in primaryUser's namespace using the proxy
			actualApp := &appstudiov1.Application{}
			err = guestUserPrimaryWsCl.Get(context.TODO(), types.NamespacedName{Name: applicationName, Namespace: primaryUserNamespace}, actualApp)

			// then
			require.NoError(t, err) // allowed since guestUser has access to primaryUser's space

			// wait for the websocket watcher which uses the proxy to receive the Application CR
			found, err := w.WaitForApplication(
				expectedApp.Name,
			)
			require.NoError(t, err)
			assert.NotEmpty(t, found)

			// Double check that the Application does exist using a regular client (non-proxy)
			createdApp := &appstudiov1.Application{}
			err = guestUser.expectedMemberCluster.Client.Get(context.TODO(), types.NamespacedName{Namespace: primaryUserNamespace, Name: applicationName}, createdApp)
			require.NoError(t, err)
			require.NotEmpty(t, createdApp)
			assert.Equal(t, expectedApp.Spec.DisplayName, createdApp.Spec.DisplayName)

			t.Run("request for shared namespace that doesn't belong to workspace context should succeed", func(t *testing.T) {
				// In this test the guest user has access to the primary user's namespace since the primary user's workspace has been shared,
				// and proxy should forward the request even if the namespace does not belong to the workspace.
				// It's up to the API server to check user's permissions. Not the proxy.

				// given
				workspaceName := guestUser.compliantUsername // guestUser's workspace
				guestUserWorkspaceURL := hostAwait.ProxyURLWithWorkspaceContext(workspaceName)
				guestUserGuestWsCl, err := hostAwait.CreateAPIProxyClient(t, guestUser.token, guestUserWorkspaceURL)
				require.NoError(t, err)

				// when
				actualApp := &appstudiov1.Application{}
				err = guestUserGuestWsCl.Get(context.TODO(), types.NamespacedName{Name: applicationName, Namespace: primaryUserNamespace}, actualApp) // primaryUser's namespace

				// then
				require.NoError(t, err)
				assert.Equal(t, primaryUserNamespace, actualApp.Namespace)
			})
		})

		t.Run("banned user", func(t *testing.T) {
			// create an user and a space
			sp, us, _ := testsupportspace.CreateSpaceWithRoleSignupResult(t, awaitilities, "admin",
				testspace.WithSpecTargetCluster(memberAwait.ClusterName),
				testspace.WithTierName("appstudio"),
			)

			// wait until the space has ProvisionedNamespaces
			sp, err := hostAwait.WaitForSpace(t, sp.Name, wait.UntilSpaceHasAnyProvisionedNamespaces())
			require.NoError(t, err)

			// ban the user
			_ = CreateBannedUser(t, hostAwait, us.UserSignup.Spec.IdentityClaims.Email)

			// wait until the user is banned
			_, err = hostAwait.
				WithRetryOptions(wait.TimeoutOption(time.Second*10), wait.RetryInterval(time.Second*2)).
				WaitForUserSignup(t, us.UserSignup.Name,
					wait.UntilUserSignupHasConditions(
						wait.ConditionSet(wait.Default(), wait.ApprovedByAdmin(), wait.Banned())...))
			require.NoError(t, err)

			// build proxy client
			proxyWorkspaceURL := hostAwait.ProxyURLWithWorkspaceContext(sp.Name)
			userProxyClient, err := hostAwait.CreateAPIProxyClient(t, us.Token, proxyWorkspaceURL)
			require.NoError(t, err)

			t.Run("banned user cannot list config maps from space", func(t *testing.T) {
				// then
				cms := corev1.ConfigMapList{}

				err = userProxyClient.List(context.TODO(), &cms, client.InNamespace(sp.Status.ProvisionedNamespaces[0].Name))
				require.ErrorContains(t, err, "user access is forbidden")
			})

			t.Run("banned user cannot create config maps into space", func(t *testing.T) {
				cm := corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-cm",
						Namespace: sp.Status.ProvisionedNamespaces[0].Name,
					},
				}

				err = userProxyClient.Create(context.TODO(), &cm)
				require.ErrorContains(t, err, "user access is forbidden")
			})
		})
	})
}

// this test will:
//  1. provision a watcher user
//  2. run a goroutine which will:
//     I. create a long-running GET call with a watch=true parameter
//     II. the call will be terminated via a context timeout
//     III. check the expected error that it was terminated via a context and not on the server side
func runWatcher(t *testing.T, awaitilities wait.Awaitilities) *sync.WaitGroup {
	// ======================================================
	// let's define two timeouts

	// contextTimeout defines the time after which the GET (watch) call will be terminated (via the context)
	// this one is the expected timeout and should be bigger than the default one that was originally set
	// for OpenShift Route and the RoundTripper inside proxy to make sure that the call is terminated
	// via the context and not by the server.
	contextTimeout := 40 * time.Second

	// this timeout will be set when initializing the go client - just to be sure that
	// there is no other value set by default and is bigger than the contextTimeout.
	clientConfigTimeout := 50 * time.Second
	// ======================================================

	t.Log("provisioning the watcher")
	watchUser := &proxyUser{
		expectedMemberCluster: awaitilities.Member1(),
		username:              "watcher",
		identityID:            uuid.New(),
	}
	createAppStudioUser(t, awaitilities, watchUser)

	proxyConfig := awaitilities.Host().CreateAPIProxyConfig(t, watchUser.token, awaitilities.Host().APIProxyURL)
	proxyConfig.Timeout = clientConfigTimeout
	watcherClient, err := kubernetes.NewForConfig(proxyConfig)
	require.NoError(t, err)

	// we need to get a list of ConfigMaps, so we can use the resourceVersion
	// of the list resource in the watch call
	t.Log("getting the first list of ConfigMaps")
	list, err := watcherClient.CoreV1().
		ConfigMaps(tenantNsName(watchUser.compliantUsername)).
		List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)

	var waitForWatcher sync.WaitGroup
	waitForWatcher.Add(1)
	// run the watch in a goroutine because it will take 40 seconds until the call is terminated
	go func() {
		defer waitForWatcher.Done()
		withTimeout, cancelFunc := context.WithTimeout(context.Background(), contextTimeout)
		defer cancelFunc()

		started := time.Now()
		t.Log("starting the watch call")
		_, err := watcherClient.RESTClient().Get().
			AbsPath(fmt.Sprintf("/api/v1/namespaces/%s/configmaps", tenantNsName(watchUser.compliantUsername))).
			Param("resourceVersion", list.GetResourceVersion()).
			Param("watch", "true").
			Do(withTimeout).
			Get()
		t.Logf("stopping the watch after %s", time.Since(started))

		require.EqualError(t, err, "unexpected error when reading response body. Please retry. Original error: context deadline exceeded", "The call should be terminated by the context timeout") // nolint:testifylint
		assert.NotContains(t, err.Error(), "unexpected EOF", "If it contains 'unexpected EOF' then the call was terminated on the server side, which is not expected.")
	}()
	return &waitForWatcher
}

func TestSpaceLister(t *testing.T) {
	t.Parallel()
	// given
	awaitilities := WaitForDeployments(t)
	hostAwait := awaitilities.Host()
	memberAwait := awaitilities.Member1()
	memberAwait2 := awaitilities.Member2()

	t.Logf("Proxy URL: %s", hostAwait.APIProxyURL)

	users := map[string]*proxyUser{
		"car": {
			expectedMemberCluster: memberAwait,
			username:              "car",
			identityID:            uuid.New(),
		},
		"bus": {
			expectedMemberCluster: memberAwait2,
			username:              "bus",
			identityID:            uuid.New(),
		},
		"bicycle": {
			expectedMemberCluster: memberAwait,
			username:              "road.bicycle", // contains a '.' that is valid in the username but should not be in the impersonation header since it should use the compliant username
			identityID:            uuid.New(),
		},
		"banneduser": {
			expectedMemberCluster: memberAwait,
			username:              "banned.user",
			identityID:            uuid.New(),
		},
	}
	appStudioTierRolesWSOption := commonproxy.WithAvailableRoles([]string{"admin", "contributor", "maintainer", "viewer"})

	// create the users before the subtests, so they exist for the duration of the whole test
	for _, user := range users {
		createAppStudioUser(t, awaitilities, user)
	}

	// share workspaces
	busSBROnCarSpace := users["car"].shareSpaceWith(t, awaitilities, users["bus"])
	bicycleSBROnCarSpace := users["car"].shareSpaceWith(t, awaitilities, users["bicycle"])
	bicycleSBROnBusSpace := users["bus"].shareSpaceWith(t, awaitilities, users["bicycle"])
	// let's also create a failing SBR so that we can check if it's being added in the bindings list
	failingSBR := users["bus"].invalidShareSpaceWith(t, awaitilities, users["car"])

	t.Run("car lists workspaces", func(t *testing.T) {
		// when
		workspaces := users["car"].listWorkspaces(t, hostAwait)

		// then
		// car should see only car's workspace
		require.Len(t, workspaces, 1)
		verifyHasExpectedWorkspace(t, expectedWorkspaceFor(t, awaitilities.Host(), "car", commonproxy.WithType("home")), workspaces...)
	})

	t.Run("car gets workspaces", func(t *testing.T) {
		t.Run("can get car workspace", func(t *testing.T) {
			// when
			workspace, err := users["car"].getWorkspace(t, hostAwait, users["car"].compliantUsername)

			// then
			require.NoError(t, err)
			verifyHasExpectedWorkspace(t, expectedWorkspaceFor(t, awaitilities.Host(), "car", commonproxy.WithType("home"), appStudioTierRolesWSOption,
				commonproxy.WithBindings([]toolchainv1alpha1.Binding{
					{MasterUserRecord: "bus", Role: "admin",
						AvailableActions: []string{"update", "delete"}, BindingRequest: &toolchainv1alpha1.BindingRequest{
							Name:      busSBROnCarSpace.GetName(),
							Namespace: busSBROnCarSpace.GetNamespace(),
						}},
					{MasterUserRecord: "car", Role: "admin", AvailableActions: []string(nil)}, // no actions since this is system generated binding
					{MasterUserRecord: "road-bicycle", Role: "admin", AvailableActions: []string{"update", "delete"}, BindingRequest: &toolchainv1alpha1.BindingRequest{
						Name:      bicycleSBROnCarSpace.GetName(),
						Namespace: bicycleSBROnCarSpace.GetNamespace(),
					}}},
				)),
				*workspace)
		})

		t.Run("can get sub-workspace", func(t *testing.T) {
			// given
			// let's create a sub-space with "car" as parent-space
			subSpace := testsupportspace.CreateSubSpace(t, awaitilities,
				testspace.WithSpecParentSpace("car"),
				testspace.WithTierName("appstudio"),
				testspace.WithSpecTargetCluster(memberAwait.ClusterName),
				testspace.WithLabel(toolchainv1alpha1.SpaceCreatorLabelKey, "car"), // atm this label is used by the spacelister to set the owner of the workspace.
			)
			subSpace, _ = testsupportspace.VerifyResourcesProvisionedForSpace(t, awaitilities, subSpace.Name)

			// when
			workspaceForCarUser, err := users["car"].getWorkspace(t, hostAwait, subSpace.GetName())
			require.NoError(t, err)
			workspaceForBicycleUser, err := users["bicycle"].getWorkspace(t, hostAwait, subSpace.GetName())
			require.NoError(t, err)
			workspaceForBusUser, err := users["bus"].getWorkspace(t, hostAwait, subSpace.GetName())
			require.NoError(t, err)

			// then
			// the spacebindings are inherited from the parent space
			// for car user
			verifyHasExpectedWorkspace(t, expectedWorkspaceFor(t, awaitilities.Host(), subSpace.GetName(), commonproxy.WithOwner("car"), appStudioTierRolesWSOption,
				commonproxy.WithType("home"),
				commonproxy.WithBindings([]toolchainv1alpha1.Binding{
					{MasterUserRecord: "bus", Role: "admin", AvailableActions: []string{"override"}},
					{MasterUserRecord: "car", Role: "admin", AvailableActions: []string{"override"}},
					{MasterUserRecord: "road-bicycle", Role: "admin", AvailableActions: []string{"override"}}},
				)),
				*workspaceForCarUser)
			// for bicycle user
			verifyHasExpectedWorkspace(t, expectedWorkspaceFor(t, awaitilities.Host(), subSpace.GetName(), commonproxy.WithOwner("car"), appStudioTierRolesWSOption,
				commonproxy.WithBindings([]toolchainv1alpha1.Binding{
					{MasterUserRecord: "bus", Role: "admin", AvailableActions: []string{"override"}},           // bus has SBR on parentSpace, so the binding can only be overridden
					{MasterUserRecord: "car", Role: "admin", AvailableActions: []string{"override"}},           // car is owner of the parentSpace, so it can only be overridden
					{MasterUserRecord: "road-bicycle", Role: "admin", AvailableActions: []string{"override"}}}, // bicycle has SBR on parentSpace, so the binding can only be overridden
				)),
				*workspaceForBicycleUser)
			// for bus user
			verifyHasExpectedWorkspace(t, expectedWorkspaceFor(t, awaitilities.Host(), subSpace.GetName(), commonproxy.WithOwner("car"), appStudioTierRolesWSOption,
				commonproxy.WithBindings([]toolchainv1alpha1.Binding{
					{MasterUserRecord: "bus", Role: "admin", AvailableActions: []string{"override"}},
					{MasterUserRecord: "car", Role: "admin", AvailableActions: []string{"override"}},
					{MasterUserRecord: "road-bicycle", Role: "admin", AvailableActions: []string{"override"}}},
				)),
				*workspaceForBusUser)
		})

		t.Run("cannot get bus workspace", func(t *testing.T) {
			// when
			workspace, err := users["car"].getWorkspace(t, hostAwait, users["bus"].compliantUsername)

			// then
			require.EqualError(t, err, "the server could not find the requested resource (get workspaces.toolchain.dev.openshift.com bus)")
			assert.Empty(t, workspace)
		})
	})

	t.Run("bus lists workspaces", func(t *testing.T) {
		// when
		workspaces := users["bus"].listWorkspaces(t, hostAwait)

		// then
		// bus should see both its own and car's workspace
		require.Len(t, workspaces, 2)
		verifyHasExpectedWorkspace(t, expectedWorkspaceFor(t, awaitilities.Host(), "bus", commonproxy.WithType("home")), workspaces...)
		verifyHasExpectedWorkspace(t, expectedWorkspaceFor(t, awaitilities.Host(), "car"), workspaces...)
	})

	t.Run("bus gets workspaces", func(t *testing.T) {
		t.Run("can get bus workspace", func(t *testing.T) {
			// when
			busWS, err := users["bus"].getWorkspace(t, hostAwait, users["bus"].compliantUsername)

			// then
			require.NoError(t, err)
			verifyHasExpectedWorkspace(t, expectedWorkspaceFor(t, awaitilities.Host(), "bus", commonproxy.WithType("home"), appStudioTierRolesWSOption,
				commonproxy.WithBindings([]toolchainv1alpha1.Binding{
					{MasterUserRecord: "bus", Role: "admin", AvailableActions: []string(nil)}, // this is system generated so no actions for the user
					// the failing SBR should be present in the list of bindings, so that the user can manage it
					{MasterUserRecord: "car", Role: "invalidRole", AvailableActions: []string{"update", "delete"}, BindingRequest: &toolchainv1alpha1.BindingRequest{
						Name:      failingSBR.GetName(),
						Namespace: failingSBR.GetNamespace(),
					}},
					{MasterUserRecord: "road-bicycle", Role: "admin", AvailableActions: []string{"update", "delete"}, BindingRequest: &toolchainv1alpha1.BindingRequest{
						Name:      bicycleSBROnBusSpace.GetName(),
						Namespace: bicycleSBROnBusSpace.GetNamespace(),
					}},
				})),

				*busWS)
		})

		t.Run("can get car workspace", func(t *testing.T) {
			// when
			carWS, err := users["bus"].getWorkspace(t, hostAwait, users["car"].compliantUsername)

			// then
			require.NoError(t, err)
			verifyHasExpectedWorkspace(t, expectedWorkspaceFor(t, awaitilities.Host(), "car", appStudioTierRolesWSOption,
				commonproxy.WithBindings([]toolchainv1alpha1.Binding{
					{MasterUserRecord: "bus", Role: "admin", AvailableActions: []string{"update", "delete"}, BindingRequest: &toolchainv1alpha1.BindingRequest{
						Name:      busSBROnCarSpace.GetName(),
						Namespace: busSBROnCarSpace.GetNamespace(),
					}},
					{MasterUserRecord: "car", Role: "admin", AvailableActions: []string(nil)}, // this is system generated so no actions for the user
					{MasterUserRecord: "road-bicycle", Role: "admin", AvailableActions: []string{"update", "delete"}, BindingRequest: &toolchainv1alpha1.BindingRequest{
						Name:      bicycleSBROnCarSpace.GetName(),
						Namespace: bicycleSBROnCarSpace.GetNamespace(),
					}}},
				),
			), *carWS)
		})
	})

	t.Run("bicycle lists workspaces", func(t *testing.T) {
		// when
		workspaces := users["bicycle"].listWorkspaces(t, hostAwait)

		// then
		// car should see only car's workspace
		require.Len(t, workspaces, 3)
		verifyHasExpectedWorkspace(t, expectedWorkspaceFor(t, awaitilities.Host(), "road-bicycle", commonproxy.WithOwner(users["bicycle"].signup.Name), commonproxy.WithType("home")), workspaces...)
		verifyHasExpectedWorkspace(t, expectedWorkspaceFor(t, awaitilities.Host(), "car"), workspaces...)
		verifyHasExpectedWorkspace(t, expectedWorkspaceFor(t, awaitilities.Host(), "bus"), workspaces...)
	})

	t.Run("bicycle gets workspaces", func(t *testing.T) {
		t.Run("can get bus workspace", func(t *testing.T) {
			// when
			busWS, err := users["bicycle"].getWorkspace(t, hostAwait, users["bus"].compliantUsername)

			// then
			require.NoError(t, err)
			verifyHasExpectedWorkspace(t, expectedWorkspaceFor(t, awaitilities.Host(), "bus", appStudioTierRolesWSOption,
				commonproxy.WithBindings([]toolchainv1alpha1.Binding{
					{MasterUserRecord: "bus", Role: "admin", AvailableActions: []string(nil)}, // this is system generated so no actions for the user
					{MasterUserRecord: "car", Role: "invalidRole", AvailableActions: []string{"update", "delete"}, BindingRequest: &toolchainv1alpha1.BindingRequest{
						Name:      failingSBR.GetName(),
						Namespace: failingSBR.GetNamespace(),
					}},
					{MasterUserRecord: "road-bicycle", Role: "admin", AvailableActions: []string{"update", "delete"}, BindingRequest: &toolchainv1alpha1.BindingRequest{
						Name:      bicycleSBROnBusSpace.GetName(),
						Namespace: bicycleSBROnBusSpace.GetNamespace(),
					}}},
				),
			), *busWS)
		})

		t.Run("can get car workspace", func(t *testing.T) {
			// when
			carWS, err := users["bicycle"].getWorkspace(t, hostAwait, users["car"].compliantUsername)

			// then
			require.NoError(t, err)
			verifyHasExpectedWorkspace(t, expectedWorkspaceFor(t, awaitilities.Host(), "car", appStudioTierRolesWSOption,
				commonproxy.WithBindings([]toolchainv1alpha1.Binding{
					{MasterUserRecord: "bus", Role: "admin", AvailableActions: []string{"update", "delete"}, BindingRequest: &toolchainv1alpha1.BindingRequest{
						Name:      busSBROnCarSpace.GetName(),
						Namespace: busSBROnCarSpace.GetNamespace(),
					}},
					{MasterUserRecord: "car", Role: "admin", AvailableActions: []string(nil)}, // this is system generated so no actions for the user
					{MasterUserRecord: "road-bicycle", Role: "admin", AvailableActions: []string{"update", "delete"}, BindingRequest: &toolchainv1alpha1.BindingRequest{
						Name:      bicycleSBROnCarSpace.GetName(),
						Namespace: bicycleSBROnCarSpace.GetNamespace(),
					}}},
				),
			), *carWS)
		})

		t.Run("can get bicycle workspace", func(t *testing.T) {
			// when
			bicycleWS, err := users["bicycle"].getWorkspace(t, hostAwait, users["bicycle"].compliantUsername)

			// then
			require.NoError(t, err)
			verifyHasExpectedWorkspace(t, expectedWorkspaceFor(t, awaitilities.Host(), "road-bicycle", commonproxy.WithOwner(users["bicycle"].signup.Name), commonproxy.WithType("home"), appStudioTierRolesWSOption,
				commonproxy.WithBindings([]toolchainv1alpha1.Binding{
					{MasterUserRecord: "road-bicycle", Role: "admin", AvailableActions: []string(nil)}}, // this is system generated so no actions for the user
				),
			), *bicycleWS)
		})
	})

	t.Run("other workspace actions not permitted", func(t *testing.T) {
		t.Run("create not allowed", func(t *testing.T) {
			// given
			workspaceToCreate := expectedWorkspaceFor(t, awaitilities.Host(), "bus")
			bicycleCl, err := hostAwait.CreateAPIProxyClient(t, users["bicycle"].token, hostAwait.APIProxyURL)
			require.NoError(t, err)

			// when
			// bicycle user tries to create a workspace
			err = bicycleCl.Create(context.TODO(), &workspaceToCreate)

			// then
			require.EqualError(t, err, fmt.Sprintf("workspaces.toolchain.dev.openshift.com is forbidden: User \"%s\" cannot create resource \"workspaces\" in API group \"toolchain.dev.openshift.com\" at the cluster scope", users["bicycle"].compliantUsername))
		})

		t.Run("delete not allowed", func(t *testing.T) {
			// given
			workspaceToDelete, err := users["bicycle"].getWorkspace(t, hostAwait, users["bicycle"].compliantUsername)
			require.NoError(t, err)
			bicycleCl, err := hostAwait.CreateAPIProxyClient(t, users["bicycle"].token, hostAwait.APIProxyURL)
			require.NoError(t, err)

			// bicycle user tries to delete a workspace
			err = bicycleCl.Delete(context.TODO(), workspaceToDelete)

			// then
			require.EqualError(t, err, fmt.Sprintf("workspaces.toolchain.dev.openshift.com \"%[1]s\" is forbidden: User \"%[1]s\" cannot delete resource \"workspaces\" in API group \"toolchain.dev.openshift.com\" at the cluster scope", users["bicycle"].compliantUsername))
		})

		t.Run("update not allowed", func(t *testing.T) {
			// when
			workspaceToUpdate := expectedWorkspaceFor(t, awaitilities.Host(), "road-bicycle", commonproxy.WithOwner(users["bicycle"].signup.Name), commonproxy.WithType("home"))
			bicycleCl, err := hostAwait.CreateAPIProxyClient(t, users["bicycle"].token, hostAwait.APIProxyURL)
			require.NoError(t, err)

			// bicycle user tries to update a workspace
			err = bicycleCl.Update(context.TODO(), &workspaceToUpdate)

			// then
			require.EqualError(t, err, fmt.Sprintf("workspaces.toolchain.dev.openshift.com \"%[1]s\" is forbidden: User \"%[1]s\" cannot update resource \"workspaces\" in API group \"toolchain.dev.openshift.com\" at the cluster scope", users["bicycle"].compliantUsername))
		})
	})

	t.Run("fix invalid SpaceRole in SBR", func(t *testing.T) {
		// we need to fix the invalid SpaceRole, otherwise it may cause flakiness as there is already a loop of reconciles based on the error,
		// and it could take too long to get ot the SBR again if (for example) the removal of the Finalizer fails
		// given
		primaryUserSpace, err := awaitilities.Host().WaitForSpace(t, users["bus"].compliantUsername)
		require.NoError(t, err)
		memberAwait, err := awaitilities.Member(primaryUserSpace.Spec.TargetCluster)
		require.NoError(t, err)

		// when
		_, err = wait.For(t, memberAwait.Awaitility, &toolchainv1alpha1.SpaceBindingRequest{}).
			Update(failingSBR.Name, failingSBR.Namespace, func(sbr *toolchainv1alpha1.SpaceBindingRequest) {
				sbr.Spec.SpaceRole = "admin"
			})

		// then
		require.NoError(t, err)
		_, err = awaitilities.Host().WaitForSpaceBinding(t, failingSBR.Spec.MasterUserRecord, primaryUserSpace.GetName())
		require.NoError(t, err)
	})

	t.Run("banned user", func(t *testing.T) {
		bannedUser := users["banneduser"]

		// share car's space with banned user
		_ = users["car"].shareSpaceWith(t, awaitilities, bannedUser)

		// initialize clients before ban so that the call to `/apis` is successful
		proxyClDefault := bannedUser.createProxyClient(t, hostAwait)
		proxyClCar, err := hostAwait.CreateAPIProxyClient(t, bannedUser.token, hostAwait.ProxyURLWithWorkspaceContext("car"))
		require.NoError(t, err)

		// ban banneduser,
		// this will be used in order to make sure this type of user doesn't have access to any of the requests in the workspace lister
		_ = CreateBannedUser(t, hostAwait, bannedUser.signup.Spec.IdentityClaims.Email)
		// wait until the user is banned
		_, err = hostAwait.
			WaitForUserSignup(t, bannedUser.signup.Name,
				wait.UntilUserSignupHasConditions(
					wait.ConditionSet(wait.Default(), wait.ApprovedByAdmin(), wait.Banned())...))
		require.NoError(t, err)

		t.Run("cannot get apis list", func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s%s", hostAwait.APIProxyURL, "/apis"), nil)
			require.NoError(t, err)

			req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", bannedUser.token))
			tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} // nolint:gosec
			client := &http.Client{Transport: tr}
			res, err := client.Do(req)
			require.NoError(t, err)
			defer res.Body.Close()

			require.Equal(t, http.StatusForbidden, res.StatusCode)
		})

		t.Run("cannot get workspace", func(t *testing.T) {
			workspaceNamespacedName := types.NamespacedName{Name: bannedUser.compliantUsername}
			err := proxyClDefault.Get(context.TODO(), workspaceNamespacedName, &toolchainv1alpha1.Workspace{})

			require.Error(t, err)
			require.ErrorContains(t, err, "user access is forbidden")
		})

		t.Run("cannot get shared workspace", func(t *testing.T) {
			workspaceNamespacedName := types.NamespacedName{Name: users["car"].compliantUsername}
			err := proxyClDefault.Get(context.TODO(), workspaceNamespacedName, &toolchainv1alpha1.Workspace{})

			require.Error(t, err)
			require.ErrorContains(t, err, "user access is forbidden")
		})

		t.Run("cannot list workspaces", func(t *testing.T) {
			err := proxyClDefault.List(context.TODO(), &toolchainv1alpha1.WorkspaceList{})

			require.Error(t, err)
			require.ErrorContains(t, err, "user access is forbidden")
		})

		t.Run("cannot list resources", func(t *testing.T) {
			workspacesTestCases := map[string]client.Client{
				"home workspace":   proxyClDefault,
				"shared workspace": proxyClCar,
			}
			for k, workspaceClient := range workspacesTestCases {
				t.Run(k, func(t *testing.T) {
					resourcesTestCases := map[string]client.ObjectList{
						"applications": &appstudiov1.ApplicationList{},
						"pods":         &corev1.PodList{},
						"configmaps":   &corev1.ConfigMapList{},
					}
					for k, objList := range resourcesTestCases {
						t.Run(k, func(t *testing.T) {
							err := workspaceClient.List(context.TODO(), objList)
							require.Error(t, err)
							require.ErrorContains(t, err, "user access is forbidden")
						})
					}
				})
			}
		})
	})
}

func tenantNsName(username string) string {
	return fmt.Sprintf("%s-tenant", username)
}

func createAppStudioUser(t *testing.T, awaitilities wait.Awaitilities, user *proxyUser) {
	// Create and approve signup
	u := NewSignupRequest(awaitilities).
		Username(user.username).
		IdentityID(user.identityID).
		ManuallyApprove().
		TargetCluster(user.expectedMemberCluster).
		EnsureMUR().
		RequireConditions(wait.ConditionSet(wait.Default(), wait.ApprovedByAdmin())...).
		Execute(t)
	user.signup = u.UserSignup
	user.token = u.Token
	tiers.MoveSpaceToTier(t, awaitilities.Host(), u.Space.Name, "appstudio")
	VerifyResourcesProvisionedForSignupWithTiers(t, awaitilities, user.signup, "deactivate30", "appstudio")
	user.compliantUsername = user.signup.Status.CompliantUsername
	_, err := awaitilities.Host().WaitForMasterUserRecord(t, user.compliantUsername, wait.UntilMasterUserRecordHasCondition(wait.Provisioned()))
	require.NoError(t, err)
}

func newWsWatcher(t *testing.T, user proxyUser, namespace, proxyURL string) *wsWatcher {
	_, err := url.Parse(proxyURL)
	require.NoError(t, err)
	return &wsWatcher{
		t:            t,
		namespace:    namespace,
		user:         user,
		proxyBaseURL: proxyURL,
	}
}

// wsWatcher represents a watcher which leverages a WebSocket connection to watch for Applications in the user's namespace.
// The connection is established with the reg-service proxy instead of direct connection to the API server.
type wsWatcher struct {
	done         chan interface{}
	interrupt    chan os.Signal
	t            *testing.T
	user         proxyUser
	namespace    string
	connection   *websocket.Conn
	proxyBaseURL string

	mu           sync.RWMutex
	receivedApps map[string]*appstudiov1.Application
}

// start creates a new WebSocket connection. The method returns a function which is to be used to close the connection when done.
func (w *wsWatcher) Start() func() {
	w.done = make(chan interface{})    // Channel to indicate that the receiverHandler is done
	w.interrupt = make(chan os.Signal) // Channel to listen for interrupt signal to terminate gracefully

	signal.Notify(w.interrupt, os.Interrupt) // Notify the interrupt channel for SIGINT

	encodedToken := base64.RawURLEncoding.EncodeToString([]byte(w.user.token))
	protocol := fmt.Sprintf("base64url.bearer.authorization.k8s.io.%s", encodedToken)

	trimmedProxyURL := strings.TrimPrefix(w.proxyBaseURL, "https://")
	socketURL := fmt.Sprintf("wss://%s/apis/appstudio.redhat.com/v1alpha1/namespaces/%s/applications?watch=true", trimmedProxyURL, tenantNsName(w.namespace))
	w.t.Logf("opening connection to '%s'", socketURL)
	dialer := &websocket.Dialer{
		Subprotocols: []string{protocol, "base64.binary.k8s.io"},
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // nolint:gosec
		},
	}

	var conn *websocket.Conn
	var resp *http.Response

	// Retry websocket connection to handle rate limiting issues
	// From OpenShift 4.19+ (using k8s 1.32), the ResilientWatchCacheInitialization feature is enabled
	// which can cause 429 rate limiting responses when watchcache is still under initialization
	err := kubewait.PollUntilContextTimeout(context.TODO(), wait.DefaultRetryInterval, wait.DefaultTimeout, true, func(ctx context.Context) (bool, error) {
		extraHeaders := make(http.Header, 1)
		extraHeaders.Add("Origin", "http://localhost")

		var err error
		conn, resp, err = dialer.Dial(socketURL, extraHeaders) // nolint:bodyclose // see `return func() {...}`

		if err != nil {
			r, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if errors.Is(err, websocket.ErrBadHandshake) {
				if resp.StatusCode == 429 {
					w.t.Logf("rate limited, retrying: %s", string(r))
					return false, nil
				}
				w.t.Logf("handshake failed with status %d / response %s", resp.StatusCode, string(r))
				return false, err
			} else {
				w.t.Logf("connection failed with status %d / response %s", resp.StatusCode, string(r))
				return false, err
			}
		}

		w.connection = conn
		return true, nil
	})
	require.NoError(w.t, err)

	w.connection = conn
	w.receivedApps = make(map[string]*appstudiov1.Application)

	go w.receiveHandler()
	go w.startMainLoop()

	return func() {
		_ = w.connection.Close()
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}
}

// startMainLoop starts the main loop for the client. Packets are sent here.
func (w *wsWatcher) startMainLoop() {
	for {
		select {
		case <-time.After(time.Duration(1) * time.Millisecond * 1000):
			// Send an echo packet every second
			err := w.connection.WriteMessage(websocket.TextMessage, []byte("Hello from e2e tests!"))
			if err != nil {
				w.t.Logf("Exiting main loop. It's normal if the connection has been closed. Reason: %s\n", err.Error())
				return
			}
		case <-w.interrupt:
			// Received a SIGINT (Ctrl + C). Terminate gracefully...
			// Close the websocket connection
			err := w.connection.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			if err != nil {
				w.t.Logf("Error during closing websocket: %s", err.Error())
				return
			}

			select {
			case <-w.done:
				w.t.Log("Receiver Channel Closed! Exiting...")
			case <-time.After(time.Duration(1) * time.Second):
				w.t.Log("Timeout in closing receiving channel. Exiting...")
			}
			return
		}
	}
}

type message struct {
	MessageType string                  `json:"type"`
	Application appstudiov1.Application `json:"object"`
}

// receiveHandler listens to the incoming messages and stores them as Applications objects
func (w *wsWatcher) receiveHandler() {
	defer close(w.done)
	for {
		_, msg, err := w.connection.ReadMessage()
		if err != nil {
			w.t.Logf("Exiting message receiving loop. It's normal if the connection has been closed. Reason: %s\n", err.Error())
			return
		}
		w.t.Logf("Received: %s", msg)
		message := message{}
		err = json.Unmarshal(msg, &message)
		require.NoError(w.t, err)
		copyApp := message.Application
		w.mu.Lock()
		if message.MessageType == "DELETED" {
			delete(w.receivedApps, copyApp.Name)
		} else {
			w.receivedApps[copyApp.Name] = &copyApp
		}
		w.mu.Unlock()
	}
}

func (w *wsWatcher) WaitForApplication(expectedAppName string) (*appstudiov1.Application, error) {
	var foundApp *appstudiov1.Application
	err := kubewait.PollUntilContextTimeout(context.TODO(), wait.DefaultRetryInterval, wait.DefaultTimeout, true, func(ctx context.Context) (bool, error) {
		defer w.mu.RUnlock()
		w.mu.RLock()
		foundApp = w.receivedApps[expectedAppName]
		return foundApp != nil, nil
	})
	return foundApp, err
}

func (w *wsWatcher) WaitForApplicationDeletion(expectedAppName string) error {
	err := kubewait.PollUntilContextTimeout(context.TODO(), wait.DefaultRetryInterval, wait.DefaultTimeout, true, func(ctx context.Context) (bool, error) {
		defer w.mu.RUnlock()
		w.mu.RLock()
		_, present := w.receivedApps[expectedAppName]
		return !present, nil
	})
	return err
}

func newApplication(applicationName, namespace string) *appstudiov1.Application {
	return &appstudiov1.Application{
		ObjectMeta: metav1.ObjectMeta{
			Name:      applicationName,
			Namespace: namespace,
		},
		Spec: appstudiov1.ApplicationSpec{
			DisplayName: fmt.Sprintf("Proxy test for user %s", namespace),
		},
	}
}

func verifyHasExpectedWorkspace(t *testing.T, expectedWorkspace toolchainv1alpha1.Workspace, actualWorkspaces ...toolchainv1alpha1.Workspace) {
	for _, actualWorkspace := range actualWorkspaces {
		if actualWorkspace.Name == expectedWorkspace.Name {
			assert.Equal(t, expectedWorkspace.Status, actualWorkspace.Status)
			assert.NotEmpty(t, actualWorkspace.ResourceVersion, "Workspace.ResourceVersion field is empty: %#v", actualWorkspace)
			assert.NotEmpty(t, actualWorkspace.Generation, "Workspace.Generation field is empty: %#v", actualWorkspace)
			assert.NotEmpty(t, actualWorkspace.CreationTimestamp, "Workspace.CreationTimestamp field is empty: %#v", actualWorkspace)
			assert.NotEmpty(t, actualWorkspace.UID, "Workspace.UID field is empty: %#v", actualWorkspace)
			return
		}
	}
	t.Errorf("expected workspace %s not found", expectedWorkspace.Name)
}

func expectedWorkspaceFor(t *testing.T, hostAwait *wait.HostAwaitility, spaceName string, additionalWSOptions ...commonproxy.WorkspaceOption) toolchainv1alpha1.Workspace {
	space, err := hostAwait.WaitForSpace(t, spaceName, wait.UntilSpaceHasAnyTargetClusterSet(), wait.UntilSpaceHasAnyTierNameSet())
	require.NoError(t, err)

	commonWSoptions := []commonproxy.WorkspaceOption{
		commonproxy.WithObjectMetaFrom(space.ObjectMeta),
		commonproxy.WithNamespaces([]toolchainv1alpha1.SpaceNamespace{
			{
				Name: spaceName + "-tenant",
				Type: "default",
			},
		}),
		commonproxy.WithOwner(spaceName),
		commonproxy.WithRole("admin"),
	}
	ws := commonproxy.NewWorkspace(spaceName,
		append(commonWSoptions, additionalWSOptions...)...,
	)
	return *ws
}

type headerKeyValue struct {
	key, value string
}
