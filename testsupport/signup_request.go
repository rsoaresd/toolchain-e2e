package testsupport

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	toolchainv1alpha1 "github.com/codeready-toolchain/api/api/v1alpha1"
	"github.com/codeready-toolchain/toolchain-common/pkg/states"
	commonauth "github.com/codeready-toolchain/toolchain-common/pkg/test/auth"
	authsupport "github.com/codeready-toolchain/toolchain-e2e/testsupport/auth"
	"github.com/codeready-toolchain/toolchain-e2e/testsupport/cleanup"
	"github.com/codeready-toolchain/toolchain-e2e/testsupport/tiers"
	"github.com/codeready-toolchain/toolchain-e2e/testsupport/wait"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

var httpClient = HTTPClient

// SignupRequest provides an API for creating a new UserSignup via the registration service REST endpoint. It operates
// with a set of sensible default values which can be overridden via its various functions.  Function chaining may
// be used to achieve an efficient "single-statement" UserSignup creation, for example:
//
// userSignupMember1, murMember1 := s.newUserRequest().
// Username("sample-username").
// Email("sample-user@redhat.com").
// ManuallyApprove().
// EnsureMUR().
// RequireConditions(wait.ConditionSet(wait.Default(), wait.ApprovedByAdmin())...).
// Execute(t)
type SignupRequest struct {
	awaitilities         wait.Awaitilities
	ensureMUR            bool
	waitForMUR           bool
	manuallyApprove      bool
	verificationRequired bool
	identityID           uuid.UUID
	username             string
	email                string
	requiredHTTPStatus   int
	targetCluster        *wait.MemberAwaitility
	conditions           []toolchainv1alpha1.Condition
	userSignup           *toolchainv1alpha1.UserSignup
	mur                  *toolchainv1alpha1.MasterUserRecord
	token                string
	originalSub          string
	userID               string
	accountID            string
	cleanupDisabled      bool
	noSpace              bool
	activationCode       string
	space                *toolchainv1alpha1.Space
	spaceTier            string
}

type SignupResult struct {
	UserSignup *toolchainv1alpha1.UserSignup
	MUR        *toolchainv1alpha1.MasterUserRecord
	Space      *toolchainv1alpha1.Space
	Token      string
}

// NewSignupRequest creates a new signup request for the registration service
func NewSignupRequest(awaitilities wait.Awaitilities) *SignupRequest {
	defaultUsername := fmt.Sprintf("testuser-%s", uuid.NewString())
	return &SignupRequest{
		awaitilities:       awaitilities,
		requiredHTTPStatus: http.StatusAccepted,
		username:           defaultUsername,
		email:              fmt.Sprintf("%s@test.com", defaultUsername),
		identityID:         uuid.New(),
	}
}

// IdentityID specifies the ID value for the user's Identity.  This value if set will be used to set both the
// "Subject" and "IdentityID" claims in the user's auth token.  If not set, a new UUID value will be used
func (r *SignupRequest) IdentityID(id uuid.UUID) *SignupRequest {
	value := id
	r.identityID = value
	return r
}

// Username specifies the username of the user
func (r *SignupRequest) Username(username string) *SignupRequest {
	r.username = username
	return r
}

// Email specifies the email address to use for the new UserSignup
func (r *SignupRequest) Email(email string) *SignupRequest {
	r.email = email
	return r
}

// OriginalSub specifies the original sub value which will be used for migrating the user to a new IdP client
func (r *SignupRequest) OriginalSub(originalSub string) *SignupRequest {
	r.originalSub = originalSub
	return r
}

func (r *SignupRequest) UserID(userID string) *SignupRequest {
	r.userID = userID
	return r
}

func (r *SignupRequest) AccountID(accountID string) *SignupRequest {
	r.accountID = accountID
	return r
}

// EnsureMUR will ensure that a MasterUserRecord is created.  It is necessary to call this function in order for
// the Execute function to return a non-nil value for its second return parameter.
func (r *SignupRequest) EnsureMUR() *SignupRequest {
	r.ensureMUR = true
	return r
}

// WaitForMUR will wait until MasterUserRecord is created
func (r *SignupRequest) WaitForMUR() *SignupRequest {
	r.waitForMUR = true
	return r
}

func (r *SignupRequest) ActivationCode(code string) *SignupRequest {
	r.activationCode = code
	return r
}

// ManuallyApprove if called will set the "approved" state to true after the UserSignup has been created
func (r *SignupRequest) ManuallyApprove() *SignupRequest {
	r.manuallyApprove = true
	return r
}

// RequireConditions specifies the condition values that the new UserSignup is required to have in order for
// the signup to be considered successful
func (r *SignupRequest) RequireConditions(conditions ...toolchainv1alpha1.Condition) *SignupRequest {
	r.conditions = conditions
	return r
}

// VerificationRequired specifies that the "verification-required" state will be set for the new UserSignup, however
// if ManuallyApprove() is also called then this will have no effect as user approval overrides the verification
// required state.
func (r *SignupRequest) VerificationRequired() *SignupRequest {
	r.verificationRequired = true
	return r
}

// TargetCluster may be provided in order to specify the user's target cluster
func (r *SignupRequest) TargetCluster(targetCluster *wait.MemberAwaitility) *SignupRequest {
	r.targetCluster = targetCluster
	return r
}

// RequireHTTPStatus may be used to override the expected HTTP response code received from the Registration Service.
// If not specified, here, the default expected value is StatusAccepted
func (r *SignupRequest) RequireHTTPStatus(httpStatus int) *SignupRequest {
	r.requiredHTTPStatus = httpStatus
	return r
}

// DisableCleanup disables automatic cleanup of the UserSignup resource after the test has completed
func (r *SignupRequest) DisableCleanup() *SignupRequest {
	r.cleanupDisabled = true
	return r
}

// NoSpace creates only a UserSignup and MasterUserRecord, Space creation will be skipped
func (r *SignupRequest) NoSpace() *SignupRequest {
	r.noSpace = true
	return r
}

// SpaceTier specifies the tier of the Space
func (r *SignupRequest) SpaceTier(spaceTier string) *SignupRequest {
	r.spaceTier = spaceTier
	return r
}

var usernamesInParallel = &namesRegistry{usernames: map[string]string{}}

type namesRegistry struct {
	sync.RWMutex
	usernames map[string]string
}

func (r *namesRegistry) add(t *testing.T, name string) {
	r.Lock()
	defer r.Unlock()
	pwd := os.Getenv("PWD")
	if !strings.HasSuffix(pwd, "parallel") {
		return
	}
	if testName, exist := r.usernames[name]; exist {
		require.Fail(t, fmt.Sprintf("The username '%s' was already used in the test '%s'", name, testName))
	}
	r.usernames[name] = t.Name()
}

// Execute executes the request against the Registration service REST endpoint. This function may only be called
// once, and must be called after all other functions. It returns SignupResult that contains the UserSignup,
// the MasterUserRecord, the Space, and the token that was generated for the request. HOWEVER, the MUR will only be
// returned here if EnsureMUR() was also called previously, otherwise a nil value will be returned.
// The space will only be returned here if 'noSpace' is true. If false, a nil value will be returned.
func (r *SignupRequest) Execute(t *testing.T) *SignupResult {
	hostAwait := r.awaitilities.Host()
	err := hostAwait.WaitUntilBaseNSTemplateTierIsUpdated(t)
	require.NoError(t, err)

	// Create a token and identity to sign up with
	usernamesInParallel.add(t, r.username)

	userIdentity := &commonauth.Identity{
		ID:       r.identityID,
		Username: r.username,
	}
	claims := []commonauth.ExtraClaim{commonauth.WithEmailClaim(r.email)}
	if r.originalSub != "" {
		claims = append(claims, commonauth.WithOriginalSubClaim(r.originalSub))
	}
	if r.userID != "" {
		claims = append(claims, commonauth.WithUserIDClaim(r.userID))
	}
	if r.accountID != "" {
		claims = append(claims, commonauth.WithAccountIDClaim(r.accountID))
	}
	r.token, err = authsupport.NewTokenFromIdentity(userIdentity, claims...)
	require.NoError(t, err)

	queryParams := map[string]string{}
	if r.noSpace {
		queryParams["no-space"] = "true"
	}

	// Call the signup POST endpoint
	invokeEndpoint(t, "POST", hostAwait.RegistrationServiceURL+"/api/v1/signup",
		r.token, "", r.requiredHTTPStatus, queryParams)

	// Wait for the UserSignup to be created
	userSignup, err := hostAwait.WaitForUserSignup(t, wait.EncodeUserIdentifier(userIdentity.Username))
	require.NoError(t, err, "failed to find UserSignup %s", userIdentity.Username)

	autoApproval := hostAwait.GetToolchainConfig(t).Spec.Host.AutomaticApproval
	if r.targetCluster != nil && autoApproval.Enabled != nil {
		require.False(t, *autoApproval.Enabled,
			"cannot specify a target cluster for new signup requests while automatic approval is enabled")
	}

	if r.manuallyApprove || r.targetCluster != nil || (r.verificationRequired != states.VerificationRequired(userSignup)) {
		doUpdate := func(instance *toolchainv1alpha1.UserSignup) {
			// We set the VerificationRequired state first, because if manuallyApprove is also set then it will
			// reset the VerificationRequired state to false.
			if r.verificationRequired != states.VerificationRequired(instance) {
				states.SetVerificationRequired(userSignup, r.verificationRequired)
			}

			if r.manuallyApprove {
				states.SetApprovedManually(instance, r.manuallyApprove)
			}
			if r.targetCluster != nil {
				instance.Spec.TargetCluster = r.targetCluster.ClusterName
			}
		}

		userSignup, err = wait.For(t, hostAwait.Awaitility, &toolchainv1alpha1.UserSignup{}).
			Update(userSignup.Name, hostAwait.Namespace, doUpdate)
		require.NoError(t, err)
	}

	t.Logf("user signup created: %+v", userSignup)

	// If any required conditions have been specified, confirm the UserSignup has them
	if len(r.conditions) > 0 {
		userSignup, err = hostAwait.WaitForUserSignup(t, userSignup.Name, wait.UntilUserSignupHasConditions(r.conditions...))
		require.NoError(t, err)
	}

	r.userSignup = userSignup

	if r.waitForMUR {
		mur, err := hostAwait.WaitForMasterUserRecord(t, userSignup.Status.CompliantUsername)
		require.NoError(t, err)
		r.mur = mur
	}

	if r.ensureMUR {
		expectedSpaceTier := tiers.GetDefaultSpaceTierName(t, hostAwait)
		if !r.noSpace {
			if r.spaceTier != "" {
				tiers.MoveSpaceToTier(t, hostAwait, userSignup.Status.CompliantUsername, r.spaceTier)
				expectedSpaceTier = r.spaceTier
			}
			space := VerifySpaceRelatedResources(t, r.awaitilities, userSignup, expectedSpaceTier)
			r.space = space
			spaceMember := GetSpaceTargetMember(t, r.awaitilities, space)
			VerifyUserRelatedResources(t, r.awaitilities, userSignup, "deactivate30", ExpectUserAccountIn(spaceMember))
		} else {
			VerifyUserRelatedResources(t, r.awaitilities, userSignup, "deactivate30", NoUserAccount())
		}
		mur, err := hostAwait.WaitForMasterUserRecord(t, userSignup.Status.CompliantUsername)
		require.NoError(t, err)
		r.mur = mur
	}

	// We also need to ensure that the UserSignup is deleted at the end of the test (if the test itself doesn't delete it)
	// and if cleanup hasn't been disabled
	if !r.cleanupDisabled {
		cleanup.AddCleanTasks(t, hostAwait.Client, r.userSignup)
	}

	return &SignupResult{
		UserSignup: userSignup,
		MUR:        r.mur,
		Space:      r.space,
		Token:      r.token,
	}
}

func invokeEndpoint(t *testing.T, method, path, authToken, requestBody string, requiredStatus int, queryParams map[string]string) map[string]interface{} {
	var reqBody io.Reader
	if requestBody != "" {
		reqBody = strings.NewReader(requestBody)
	}
	req, err := http.NewRequest(method, path, reqBody)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("content-type", "application/json")

	if len(queryParams) > 0 {
		q := req.URL.Query()
		for key, val := range queryParams {
			q.Add(key, val)
		}
		req.URL.RawQuery = q.Encode()
	}

	req.Close = true
	resp, err := httpClient.Do(req) // nolint:bodyclose // see `defer Close(t, resp)`
	require.NoError(t, err, "error posting signup request.\nmethod : %s\npath : %s\nauthToken : %s\nbody : %s", method, path, authToken, requestBody)
	defer Close(t, resp)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NotNil(t, body)
	require.Equal(t, requiredStatus, resp.StatusCode, "unexpected response status with body: %s", body)

	mp := make(map[string]interface{})
	if len(body) > 0 {
		err = json.Unmarshal(body, &mp)
		require.NoError(t, err)
	}
	return mp
}

func Close(t *testing.T, resp *http.Response) {
	if resp == nil {
		return
	}
	_, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	err = resp.Body.Close()
	require.NoError(t, err)
}
