package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBlockedOnlyByDiggerApply_AllRequiredPassingExceptDiggerApply(t *testing.T) {
	state := prMergeState{
		ReviewDecision: "APPROVED",
		Contexts: []requiredCheckContext{
			{Name: "Validate Platform Metadata", State: "SUCCESS", IsRequired: true},
			{Name: "Wiz IaC Scanner", State: "NEUTRAL", IsRequired: true},
			{Name: "digger/plan", State: "SUCCESS", IsRequired: true},
			{Name: "digger/apply", State: "", IsRequired: true},
		},
	}

	assert.True(t, blockedOnlyByDiggerApply(state))
}

func TestBlockedOnlyByDiggerApply_RequiredCheckFailing(t *testing.T) {
	state := prMergeState{
		ReviewDecision: "APPROVED",
		Contexts: []requiredCheckContext{
			{Name: "Validate Platform Metadata", State: "FAILURE", IsRequired: true},
			{Name: "digger/apply", State: "", IsRequired: true},
		},
	}

	assert.False(t, blockedOnlyByDiggerApply(state))
}

func TestBlockedOnlyByDiggerApply_NonRequiredCheckFailingIsIgnored(t *testing.T) {
	// Reproduces the real scenario hit during testing: our own workflow's native
	// job-status check ("Digger") and Digger's own project-scoped status check
	// (e.g. "digger-version-test-poc/apply") both failed, but neither is a required
	// check, so they must not block apply.
	state := prMergeState{
		ReviewDecision: "APPROVED",
		Contexts: []requiredCheckContext{
			{Name: "Digger", State: "FAILURE", IsRequired: false},
			{Name: "digger-version-test-poc/apply", State: "FAILURE", IsRequired: false},
			{Name: "Wiz IaC Scanner", State: "NEUTRAL", IsRequired: true},
			{Name: "digger/apply", State: "", IsRequired: true},
		},
	}

	assert.True(t, blockedOnlyByDiggerApply(state))
}

func TestBlockedOnlyByDiggerApply_SkippedAndNeutralAreAccepted(t *testing.T) {
	state := prMergeState{
		ReviewDecision: "APPROVED",
		Contexts: []requiredCheckContext{
			{Name: "Validate Platform Metadata", State: "SKIPPED", IsRequired: true},
			{Name: "Wiz IaC Scanner", State: "NEUTRAL", IsRequired: true},
			{Name: "digger/apply", State: "", IsRequired: true},
		},
	}

	assert.True(t, blockedOnlyByDiggerApply(state))
}

func TestBlockedOnlyByDiggerApply_NoContexts(t *testing.T) {
	assert.True(t, blockedOnlyByDiggerApply(prMergeState{}))
}

func TestBlockedOnlyByDiggerApply_ReviewRequiredBlocks(t *testing.T) {
	// The case CODEOWNERS enforcement rests on: with require_code_owner_review enabled, an
	// approval from a non-owner still leaves reviewDecision at REVIEW_REQUIRED. Every required
	// check is green, so without the review gate this PR would be waved through.
	state := prMergeState{
		ReviewDecision: "REVIEW_REQUIRED",
		Contexts: []requiredCheckContext{
			{Name: "lint-and-fix", State: "SUCCESS", IsRequired: true},
			{Name: "digger/plan", State: "SUCCESS", IsRequired: true},
			{Name: "digger/apply", State: "", IsRequired: true},
		},
	}

	assert.False(t, blockedOnlyByDiggerApply(state))
}

func TestBlockedOnlyByDiggerApply_ChangesRequestedBlocks(t *testing.T) {
	state := prMergeState{
		ReviewDecision: "CHANGES_REQUESTED",
		Contexts: []requiredCheckContext{
			{Name: "digger/plan", State: "SUCCESS", IsRequired: true},
			{Name: "digger/apply", State: "", IsRequired: true},
		},
	}

	assert.False(t, blockedOnlyByDiggerApply(state))
}

func TestBlockedOnlyByDiggerApply_EmptyReviewDecisionMeansNoReviewGate(t *testing.T) {
	// A branch that requires no reviews reports reviewDecision as null; that must not be
	// mistaken for an unmet review requirement.
	state := prMergeState{
		Contexts: []requiredCheckContext{
			{Name: "digger/plan", State: "SUCCESS", IsRequired: true},
			{Name: "digger/apply", State: "", IsRequired: true},
		},
	}

	assert.True(t, blockedOnlyByDiggerApply(state))
}
