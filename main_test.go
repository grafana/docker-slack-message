package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newTestClient(fn roundTripFunc) *http.Client {
	return &http.Client{Transport: fn}
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
