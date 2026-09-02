package github

import (
	"fmt"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/diggerhq/digger/libs/ci"
)

type MockCiService struct {
	CommentsPerPr map[int][]*ci.Comment
}

func NewMockCiService() MockCiService {
	return MockCiService{CommentsPerPr: map[int][]*ci.Comment{}}
}

func (t MockCiService) GetUserTeams(organisation string, user string) ([]string, error) {
	return nil, nil
}

func (t MockCiService) GetApprovals(prNumber int) ([]string, error) {
	return []string{}, nil
}

func (t MockCiService) GetChangedFiles(prNumber int) ([]string, error) {
	return nil, nil
}

// GitHub answers an oversized body with a 422 and posts nothing.
func (t MockCiService) PublishComment(prNumber int, comment string) (*ci.Comment, error) {
	if utf8.RuneCountInString(comment) > ci.GithubCommentMaxLength {
		return nil, fmt.Errorf("422 Unprocessable Entity: Body is too long (maximum is %d characters)", ci.GithubCommentMaxLength)
	}

	latestId := 0

	for _, comments := range t.CommentsPerPr {
		for _, c := range comments {
			id, _ := c.GetIdAsInt()
			if id > latestId {
				latestId = id
			}
		}
	}

	newComment := &ci.Comment{Id: strconv.Itoa(latestId + 1), Body: &comment}
	t.CommentsPerPr[prNumber] = append(t.CommentsPerPr[prNumber], newComment)

	return newComment, nil
}

func (t MockCiService) ListIssues() ([]*ci.Issue, error) {
	return nil, fmt.Errorf("implement me")
}

func (t MockCiService) PublishIssue(title string, body string, labels *[]string) (int64, error) {
	return 0, fmt.Errorf("implement me")
}

func (svc MockCiService) UpdateIssue(ID int64, title string, body string) (int64, error) {
	return 0, fmt.Errorf("implement me")
}

func (t MockCiService) SetStatus(prNumber int, status string, statusContext string) error {
	return nil
}

func (t MockCiService) GetCombinedPullRequestStatus(prNumber int) (string, error) {
	return "", nil
}

func (t MockCiService) MergePullRequest(prNumber int, mergeStrategy string) error {
	return nil
}

func (t MockCiService) IsMergeable(prNumber int) (bool, error) {
	return true, nil
}

func (t MockCiService) IsMerged(prNumber int) (bool, error) {
	return false, nil
}

func (t MockCiService) DownloadLatestPlans(prNumber int) (string, error) {
	return "", nil
}

func (t MockCiService) IsClosed(prNumber int) (bool, error) {
	return false, nil
}

// TODO implement me
func (t MockCiService) IsDivergedFromBranch(sourceBranch string, targetBranch string) (bool, error) {
	return false, nil
}

func (t MockCiService) GetComments(prNumber int) ([]ci.Comment, error) {
	comments := []ci.Comment{}
	for _, c := range t.CommentsPerPr[prNumber] {
		comments = append(comments, *c)
	}
	return comments, nil
}

func (t MockCiService) EditComment(prNumber int, id string, comment string) error {
	if utf8.RuneCountInString(comment) > ci.GithubCommentMaxLength {
		return fmt.Errorf("422 Unprocessable Entity: Body is too long (maximum is %d characters)", ci.GithubCommentMaxLength)
	}
	for _, comments := range t.CommentsPerPr {
		for _, c := range comments {
			if c.Id == id {
				c.Body = &comment
				return nil
			}
		}
	}
	return nil
}

func (t MockCiService) DeleteComment(id string) error {
	for prNumber, comments := range t.CommentsPerPr {
		t.CommentsPerPr[prNumber] = slices.DeleteFunc(comments, func(c *ci.Comment) bool {
			return c.Id == id
		})
	}
	return nil
}

func (t MockCiService) CreateCommentReaction(id string, reaction string) error {
	// TODO implement me
	return nil
}

func (t MockCiService) GetBranchName(prNumber int) (string, string, string, string, error) {
	return "", "", "", "", nil
}

func (svc MockCiService) SetOutput(prNumber int, key string, value string) error {
	//TODO implement me
	return nil
}
