package ksctl

// import (
// 	"os"
// 	"os/exec"
// 	"testing"

// 	. "github.com/codeready-toolchain/toolchain-e2e/testsupport"
// 	"github.com/codeready-toolchain/toolchain-e2e/testsupport/tiers"
// 	"github.com/codeready-toolchain/toolchain-e2e/testsupport/wait"
// 	"github.com/stretchr/testify/assert"
// 	"github.com/stretchr/testify/require"
// )

// func TestPromoteUserCommand(t *testing.T) {
// 	t.Parallel()

// 	awaitilities := WaitForDeployments(t)
// 	hostAwait := awaitilities.Host()

// 	username := "user-to-ban"
// 	user := NewSignupRequest(awaitilities).
// 		Username(username).
// 		Email(username + "@redhat.com").
// 		ManuallyApprove().
// 		RequireConditions(wait.ConditionSet(wait.Default(), wait.ApprovedByAdmin())...).
// 		EnsureMUR().
// 		Execute(t)

// 	// wait for the user to be provisioned for the first time
// 	VerifyResourcesProvisionedForSignup(t, awaitilities, user.UserSignup)

// 	source := os.Getenv("KSCTL_BIN_DIR")
// 	murName := ""
// 	targetTier := "deactivate180"

// 	// ksctl promote-user <masteruserrecord-name> <target-tier>
// 	args := []string{"promote-user", murName, targetTier}

// 	// run promote-user command
// 	cmd := exec.Command(source, args...)
// 	output, err := cmd.Output()
// 	require.NoError(t, err)
// }
