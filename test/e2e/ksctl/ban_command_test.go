package ksctl

import (
	"fmt"
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

	// source := os.Getenv("KSCTL_BIN_DIR")
	// source := "/Users/rsoaresd/toolchain-e2e/build/_output/bin/ksctl"
	source := "ksctl"
	reasonToBan := "test-ban-command"
	// ksctl ban <usersignup-name> <ban-reason>
	args := []string{"ban", username, reasonToBan, "--config", "/Users/rsoaresd/Documents/go/src/github.com/kubesaw/ksctl/out/config/first-admin/ksctl.yaml"}

	// run ban command
	cmd := exec.Command(source, args...) //#nosec
	// var stderr bytes.Buffer
	// cmd.Stderr = &stderr

	stdOutput, err := cmd.CombinedOutput()
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			fmt.Println("output", strings.Split(string(stdOutput), "\n"))
		}
	}

	// output, err := cmd.Output()
	// if err != nil {
	// 	fmt.Printf("Command failed with error: %v\n", err)
	// 	fmt.Printf("Stderr:\n%s\n", stderr.String()) // Only stderr output
	// }
	require.NoError(t, err)
	// fmt.Println("aqui vai outpyt", string(output))

	// // check ban command output
	// assert.NotEmpty(t, output)

	// verify user was banned
	userEmailHash := user.UserSignup.Labels[toolchainv1alpha1.UserSignupUserEmailHashLabelKey]
	bannedUser, err := hostAwait.WaitForBannedUser(t, userEmailHash)
	require.NoError(t, err)
	assert.Equal(t, bannedUser.Spec.Reason, reasonToBan)
	cleanup.AddCleanTasks(t, hostAwait.Client, bannedUser)
}
