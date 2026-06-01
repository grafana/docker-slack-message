package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newTestClient(fn roundTripFunc) *http.Client {
	return &http.Client{Transport: fn}
}

// fakeSlackClient implements slackMembershipClient, recording calls and returning
// configurable values/errors.
type fakeSlackClient struct {
	inviteErr  error
	invited    []string // user IDs passed to invite (one per call)
	openErr    error
	openChanID string
	postErr    error
	posted     []string // DM channel IDs posted to
}

func (f *fakeSlackClient) InviteUsersToConversationContext(_ context.Context, _ string, users ...string) (*slack.Channel, error) {
	f.invited = append(f.invited, users...)
	if f.inviteErr != nil {
		return nil, f.inviteErr
	}
	return &slack.Channel{}, nil
}

func (f *fakeSlackClient) OpenConversationContext(_ context.Context, _ *slack.OpenConversationParameters) (*slack.Channel, bool, bool, error) {
	if f.openErr != nil {
		return nil, false, false, f.openErr
	}
	ch := &slack.Channel{}
	ch.ID = f.openChanID
	return ch, false, false, nil
}

func (f *fakeSlackClient) PostMessageContext(_ context.Context, channelID string, _ ...slack.MsgOption) (string, string, error) {
	if f.postErr != nil {
		return "", "", f.postErr
	}
	f.posted = append(f.posted, channelID)
	return channelID, "123.456", nil
}

func TestContainsGitHubUsername(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		message  string
		username string
		matches  bool
	}{
		{name: "exact match", message: "deployed by octocat", username: "octocat", matches: true},
		{name: "case insensitive", message: "OCTOCAT shipped", username: "octocat", matches: true},
		{name: "substring should fail", message: "octocategories only", username: "octocat", matches: false},
		{name: "surrounded by punctuation", message: "(octocat)", username: "octocat", matches: true},
		{name: "missing username", message: "deployed", username: "octocat", matches: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := containsGitHubUsername(tt.message, tt.username)
			require.Equal(t, tt.matches, got, "containsGitHubUsername(%q, %q)", tt.message, tt.username)
		})
	}
}

func TestPrependSlackMention(t *testing.T) {
	t.Parallel()

	endpoint := "https://getslackuserid.local/"

	successClient := newTestClient(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != endpoint+"octocat" {
			t.Fatalf("unexpected URL: %s", req.URL.String())
		}
		body := io.NopCloser(strings.NewReader(`{"slack_user_id":"U12345"}`))
		return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
	})

	notFoundClient := newTestClient(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("missing"))}, nil
	})

	errorClient := newTestClient(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("boom")
	})

	tests := []struct {
		name       string
		cfg        config
		httpClient *http.Client
		expected   string
	}{
		{
			name: "mentions disabled",
			cfg: config{
				Message:         "hello from octocat",
				GitHubUser:      "octocat",
				EnableMentions:  false,
				MappingEndpoint: endpoint,
			},
			expected: "hello from octocat",
		},
		{
			name: "empty github user",
			cfg: config{
				Message:         "hello",
				EnableMentions:  true,
				MappingEndpoint: endpoint,
			},
			expected: "hello",
		},
		{
			name: "github user missing in message",
			cfg: config{
				Message:         "hello hubot",
				GitHubUser:      "octocat",
				EnableMentions:  true,
				MappingEndpoint: endpoint,
			},
			expected: "hello hubot",
		},
		{
			name: "successful mention prepend",
			cfg: config{
				Message:         "hello from octocat",
				GitHubUser:      "octocat",
				EnableMentions:  true,
				MappingEndpoint: endpoint,
			},
			httpClient: successClient,
			expected:   "<@U12345>: hello from octocat",
		},
		{
			name: "mapping not found",
			cfg: config{
				Message:         "hello from octocat",
				GitHubUser:      "octocat",
				EnableMentions:  true,
				MappingEndpoint: endpoint,
			},
			httpClient: notFoundClient,
			expected:   "hello from octocat",
		},
		{
			name: "mapping request error",
			cfg: config{
				Message:         "hello from octocat",
				GitHubUser:      "octocat",
				EnableMentions:  true,
				MappingEndpoint: endpoint,
			},
			httpClient: errorClient,
			expected:   "hello from octocat",
		},
		{
			name: "mapping endpoint missing",
			cfg: config{
				Message:        "hello from octocat",
				GitHubUser:     "octocat",
				EnableMentions: true,
			},
			httpClient: successClient,
			expected:   "hello from octocat",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := prependSlackMention(context.Background(), tt.cfg, tt.httpClient)
			require.Equal(t, tt.expected, got)
		})
	}
}

func TestFetchSlackUserID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		client      *http.Client
		expectedID  string
		expectError bool
		errCheck    func(*testing.T, error)
	}{
		{
			name: "success",
			client: newTestClient(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"slack_user_id":"U4242"}`)),
				}, nil
			}),
			expectedID: "U4242",
		},
		{
			name: "not found",
			client: newTestClient(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Body:       io.NopCloser(strings.NewReader("missing")),
				}, nil
			}),
			expectError: true,
			errCheck: func(t *testing.T, err error) {
				var target *slackUserNotFoundError
				require.ErrorAs(t, err, &target)
			},
		},
		{
			name: "unexpected status",
			client: newTestClient(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Body:       io.NopCloser(strings.NewReader("boom")),
				}, nil
			}),
			expectError: true,
		},
		{
			name: "decode error",
			client: newTestClient(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("not json")),
				}, nil
			}),
			expectError: true,
		},
		{
			name: "request failure",
			client: newTestClient(func(req *http.Request) (*http.Response, error) {
				return nil, errors.New("network down")
			}),
			expectError: true,
		},
	}

	endpoint := "https://getslackuserid.local/"

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := fetchSlackUserID(context.Background(), tt.client, "octocat", endpoint)
			if tt.expectError {
				require.Error(t, err)
				if tt.errCheck != nil {
					tt.errCheck(t, err)
				}
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.expectedID, got)
		})
	}
}

func TestExtractMentionedUserIDs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		message  string
		expected []string
	}{
		{name: "single user", message: "hi <@U123>", expected: []string{"U123"}},
		{name: "grid user", message: "hi <@W123>", expected: []string{"W123"}},
		{name: "labeled mention", message: "hi <@U123|alice>", expected: []string{"U123"}},
		{name: "multiple and dedup", message: "<@U123> and <@U456> and <@U123> again", expected: []string{"U123", "U456"}},
		{name: "prepended plus raw", message: "<@U999>: ping <@U123>", expected: []string{"U999", "U123"}},
		{name: "no mentions", message: "nothing here", expected: nil},
		{name: "ignores channel mention", message: "see <#C123>", expected: nil},
		{name: "ignores special mention", message: "hey <!here>", expected: nil},
		{name: "ignores malformed", message: "broken <@123>", expected: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, extractMentionedUserIDs(tt.message))
		})
	}
}

func TestParseMembershipMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		expected  membershipMode
		expectErr bool
	}{
		{name: "empty defaults to none", input: "", expected: membershipModeNone},
		{name: "none", input: "none", expected: membershipModeNone},
		{name: "invite", input: "invite", expected: membershipModeInvite},
		{name: "notify", input: "notify", expected: membershipModeNotify},
		{name: "unknown errors", input: "bogus", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseMembershipMode(tt.input)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.expected, got)
		})
	}
}

func TestEnsureMentionMembership(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		mode        membershipMode
		message     string
		client      *fakeSlackClient
		wantInvited []string
		wantPosted  []string
	}{
		{
			name:    "mode none does nothing",
			mode:    membershipModeNone,
			message: "ping <@U123>",
			client:  &fakeSlackClient{},
		},
		{
			name:    "no mentions does nothing",
			mode:    membershipModeInvite,
			message: "nobody here",
			client:  &fakeSlackClient{},
		},
		{
			name:        "invite invites each user",
			mode:        membershipModeInvite,
			message:     "<@U123> and <@U456>",
			client:      &fakeSlackClient{},
			wantInvited: []string{"U123", "U456"},
		},
		{
			name:        "invite already_in_channel is non-fatal",
			mode:        membershipModeInvite,
			message:     "<@U123>",
			client:      &fakeSlackClient{inviteErr: errors.New("already_in_channel")},
			wantInvited: []string{"U123"},
		},
		{
			name:        "invite other error is non-fatal",
			mode:        membershipModeInvite,
			message:     "<@U123>",
			client:      &fakeSlackClient{inviteErr: errors.New("not_in_channel")},
			wantInvited: []string{"U123"},
		},
		{
			name:       "notify DMs each user",
			mode:       membershipModeNotify,
			message:    "<@U123> and <@U456>",
			client:     &fakeSlackClient{openChanID: "D999"},
			wantPosted: []string{"D999", "D999"},
		},
		{
			name:       "notify open error skips that user",
			mode:       membershipModeNotify,
			message:    "<@U123>",
			client:     &fakeSlackClient{openErr: errors.New("user_not_found")},
			wantPosted: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ensureMentionMembership(context.Background(), tt.client, tt.mode, "C123", tt.message)
			require.Equal(t, tt.wantInvited, tt.client.invited)
			require.Equal(t, tt.wantPosted, tt.client.posted)
		})
	}
}
