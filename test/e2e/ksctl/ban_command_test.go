package ksctl

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
	"testing"

	toolchainv1alpha1 "github.com/codeready-toolchain/api/api/v1alpha1"
	. "github.com/codeready-toolchain/toolchain-e2e/testsupport"
	"github.com/codeready-toolchain/toolchain-e2e/testsupport/cleanup"
	"github.com/codeready-toolchain/toolchain-e2e/testsupport/wait"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBanCommand(t *testing.T) {
	t.Parallel()

	awaitilities := WaitForDeployments(t)
	hostAwait := awaitilities.Host()

	username := "user-to-ban"
	user := NewSignupRequest(awaitilities).
		Username(username).
		Email(username + "@redhat.com").
		ManuallyApprove().
		RequireConditions(wait.ConditionSet(wait.Default(), wait.ApprovedByAdmin())...).
		EnsureMUR().
		Execute(t)

	source := "ksctl"
	reasonToBan := "test-ban-command"
	// ksctl ban <usersignup-name> <ban-reason>
	args := []string{"ban", username, reasonToBan}

	// run ban command
	cmd := exec.Command(source, args...) //#nosec

	// confirm that we want to ban
	input := []byte("y\n")
	cmd.Stdin = bytes.NewReader(input)

	stdOutput, err := cmd.CombinedOutput()
	cmdOutput := string(stdOutput)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Logf("command output: %s", strings.Split(cmdOutput, "\n"))
		}
	}
	require.NoError(t, err)
	assert.Contains(t, cmdOutput, "!!!  DANGER ZONE  !!!")
	assert.Contains(t, cmdOutput, "Are you sure that you want to ban the user with the UserSignup by creating BannedUser resource that are both above?")
	assert.Contains(t, cmdOutput, "UserSignup has been banned")

	// verify user was banned
	userEmailHash := user.UserSignup.Labels[toolchainv1alpha1.UserSignupUserEmailHashLabelKey]
	bannedUser, err := hostAwait.WaitForBannedUser(t, userEmailHash)
	require.NoError(t, err)
	assert.Equal(t, bannedUser.Spec.Reason, reasonToBan)
	cleanup.AddCleanTasks(t, hostAwait.Client, bannedUser)
}
