package github

import (
	"strings"
	"testing"

	"github.com/diggerhq/digger/libs/ci"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newMockCiService() MockCiService {
	return MockCiService{CommentsPerPr: map[int][]*ci.Comment{}}
}

func TestMockPublishCommentReturnsTheIdItStored(t *testing.T) {
	svc := newMockCiService()

	published, err := svc.PublishComment(1, "first")
	require.NoError(t, err)
	require.NotNil(t, published)

	comments, err := svc.GetComments(1)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, comments[0].Id, published.Id,
		"callers persist the returned id and edit that comment later, so it must be the one that was stored")

	// The returned id must be the handle an edit works against.
	require.NoError(t, svc.EditComment(1, published.Id, "edited"))
	comments, err = svc.GetComments(1)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	require.NotNil(t, comments[0].Body)
	assert.Equal(t, "edited", *comments[0].Body)
}

func TestMockPublishCommentIssuesDistinctIds(t *testing.T) {
	svc := newMockCiService()

	first, err := svc.PublishComment(1, "first")
	require.NoError(t, err)
	second, err := svc.PublishComment(1, "second")
	require.NoError(t, err)
	otherPr, err := svc.PublishComment(2, "third")
	require.NoError(t, err)

	ids := []string{first.Id, second.Id, otherPr.Id}
	assert.Len(t, lo.Uniq(ids), 3, "ids must be unique across the whole mock, got %v", ids)
}

func TestMockRejectsOversizedComments(t *testing.T) {
	svc := newMockCiService()

	existing, err := svc.PublishComment(1, "small enough")
	require.NoError(t, err)

	oversized := strings.Repeat("x", githubCommentMaxLength+1)

	_, err = svc.PublishComment(1, oversized)
	assert.Error(t, err, "GitHub answers an oversized body with a 422")

	err = svc.EditComment(1, existing.Id, oversized)
	assert.Error(t, err, "GitHub answers an oversized body with a 422")

	comments, err := svc.GetComments(1)
	require.NoError(t, err)
	require.Len(t, comments, 1, "a rejected publish must not store anything")
	require.NotNil(t, comments[0].Body)
	assert.Equal(t, "small enough", *comments[0].Body, "a rejected edit must not change the body")
}

func TestMockAcceptsACommentExactlyAtTheLimit(t *testing.T) {
	svc := newMockCiService()

	_, err := svc.PublishComment(1, strings.Repeat("x", githubCommentMaxLength))
	assert.NoError(t, err, "the limit is inclusive")
}

func TestMockDeleteCommentRemovesIt(t *testing.T) {
	svc := newMockCiService()

	first, err := svc.PublishComment(1, "first")
	require.NoError(t, err)
	second, err := svc.PublishComment(1, "second")
	require.NoError(t, err)

	require.NoError(t, svc.DeleteComment(first.Id))

	comments, err := svc.GetComments(1)
	require.NoError(t, err)
	require.Len(t, comments, 1, "the deleted comment must be gone")
	assert.Equal(t, second.Id, comments[0].Id)
}
