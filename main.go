package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/kelseyhightower/envconfig"
	"github.com/slack-go/slack"
)

type config struct {
	// Content
	Color   string `envconfig:"SLACK_COLOR" default:"#008000"`
	Title   string `envconfig:"SLACK_TITLE"`
	Message string `envconfig:"SLACK_MESSAGE"`
	Context string `envconfig:"SLACK_CONTEXT"`

	AlsoSendToChannel bool   `envconfig:"SLACK_ALSO_SEND_TO_CHANNEL" default:"false"`
	Channel           string `envconfig:"SLACK_CHANNEL" required:"true"`
	OutputDir         string `envconfig:"SLACK_OUTPUT_DIR" default:"/app/outputs"`
	ThreadTs          string `envconfig:"SLACK_THREAD_TS"`
	UpdateTs          string `envconfig:"SLACK_UPDATE_MESSAGE_TS"`
	DeleteTs          string `envconfig:"SLACK_DELETE_MESSAGE_TS"`
	Token             string `envconfig:"SLACK_TOKEN" required:"true"`
	GitHubUser        string `envconfig:"GH_USER"`
	EnableMentions    bool   `envconfig:"ENABLE_SLACK_MENTIONS"`
	MappingEndpoint   string `envconfig:"GITHUB_SLACK_MAPPING_ENDPOINT"`

	// MentionMembershipMode controls what happens to Slack users tagged in the
	// message: "none" (default, no-op), "invite" (add them to the channel) or
	// "notify" (DM them a link to the channel).
	MentionMembershipMode string `envconfig:"SLACK_MENTION_MEMBERSHIP_MODE" default:"none"`
}

const (
	slackMentionTimeout   = 30 * time.Second
	usernameBoundaryClass = `[^A-Za-z0-9_-]`
)

type membershipMode string

const (
	membershipModeNone   membershipMode = "none"
	membershipModeInvite membershipMode = "invite"
	membershipModeNotify membershipMode = "notify"
)

func parseMembershipMode(s string) (membershipMode, error) {
	switch membershipMode(s) {
	case "", membershipModeNone:
		return membershipModeNone, nil
	case membershipModeInvite, membershipModeNotify:
		return membershipMode(s), nil
	default:
		return "", fmt.Errorf("invalid SLACK_MENTION_MEMBERSHIP_MODE %q (valid: none, invite, notify)", s)
	}
}

// slackMembershipClient is the subset of *slack.Client used to invite or notify
// mentioned users. Defining it as an interface keeps the API calls thin and lets
// tests inject a fake. *slack.Client satisfies it.
type slackMembershipClient interface {
	InviteUsersToConversationContext(ctx context.Context, channelID string, users ...string) (*slack.Channel, error)
	OpenConversationContext(ctx context.Context, params *slack.OpenConversationParameters) (*slack.Channel, bool, bool, error)
	PostMessageContext(ctx context.Context, channelID string, options ...slack.MsgOption) (string, string, error)
}

// slackMentionRe matches Slack user mentions: <@U012ABC> or <@W012ABC> (W = grid
// users), optionally with a label as in <@U012ABC|alice>. It deliberately does
// not match channel mentions (<#C...>) or special mentions (<!here>).
var slackMentionRe = regexp.MustCompile(`<@([UW][A-Z0-9]+)(?:\|[^>]*)?>`)

type slackUserNotFoundError struct {
	GitHubUser string
}

func (e *slackUserNotFoundError) Error() string {
	return fmt.Sprintf("slack user not found in mapping API for %s", e.GitHubUser)
}

func (c config) String() string {
	c.Token = c.Token[:8] + "..."
	json, _ := json.MarshalIndent(c, "", "  ")
	return string(json)
}

func main() {
	var cfg config
	envconfig.MustProcess("", &cfg)
	slog.Info("Config loaded", "config", cfg.String())

	mode, err := parseMembershipMode(cfg.MentionMembershipMode)
	if err != nil {
		slog.Error("Invalid configuration", "error", err)
		os.Exit(1)
	}

	slackClient := slack.New(cfg.Token)
	httpClient := &http.Client{
		Timeout: slackMentionTimeout,
	}

	cfg.Message = prependSlackMention(context.Background(), cfg, httpClient)

	if cfg.UpdateTs != "" && cfg.DeleteTs != "" {
		slog.Error("Cannot update and delete a message at the same time")
		os.Exit(1)
	}

	// Send the message
	options := []slack.MsgOption{content(cfg)}
	if cfg.UpdateTs != "" {
		options = append(options, slack.MsgOptionUpdate(cfg.UpdateTs))
	} else if cfg.DeleteTs != "" {
		options = append(options, slack.MsgOptionDelete(cfg.DeleteTs))
	} else if cfg.ThreadTs != "" {
		options = append(options, slack.MsgOptionTS(cfg.ThreadTs))
		if cfg.AlsoSendToChannel {
			options = append(options, slack.MsgOptionBroadcast())
		}
	}
	channelID, messageTs, _, err := slackClient.SendMessage(cfg.Channel, options...)
	if err != nil {
		panic(err)
	}

	// Best-effort: invite or notify any users tagged in the message who may not
	// be in the channel. Only for new messages/replies — for update the original
	// send already handled it, and a delete has nothing to be mentioned in.
	if cfg.UpdateTs == "" && cfg.DeleteTs == "" {
		ensureMentionMembership(context.Background(), slackClient, mode, channelID, cfg.Message)
	}

	// threadTs is the timestamp of the root message of a thread.
	// If the message is a reply (threadTs passed in config), then keep the threadTs.
	// If not, this is a new thread, so the root message is the current message,
	// and the threadTs is the same as the messageTs.
	threadTs := cfg.ThreadTs
	if threadTs == "" {
		threadTs = messageTs
	}

	// Write the channelID, messageTs and threadTs to a file to be reused in another container (For example, steps in Argo Workflows)
	// ThreadTs: timestamp of the root message of a thread
	// MessageTs: timestamp of the message (root or reply)
	// ChannelID: ID of the channel where the message was sent. This is required to update messages. The API requires the ID, not the name.
	if cfg.OutputDir != "" {
		slog.Info("channel-id written", "channel_id", channelID)
		if err := os.WriteFile(filepath.Join(cfg.OutputDir, "channel-id"), []byte(channelID), 0644); err != nil {
			panic(err)
		}
		slog.Info("message-ts written", "message_ts", messageTs)
		if err := os.WriteFile(filepath.Join(cfg.OutputDir, "message-ts"), []byte(messageTs), 0644); err != nil {
			panic(err)
		}
		slog.Info("thread-ts written", "thread_ts", threadTs)
		if err := os.WriteFile(filepath.Join(cfg.OutputDir, "thread-ts"), []byte(threadTs), 0644); err != nil {
			panic(err)
		}
	}
}

func content(cfg config) slack.MsgOption {
	var blocks []slack.Block
	if cfg.Title != "" {
		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, "*"+cfg.Title+"*", false, false), nil, nil,
		))
	}
	if cfg.Message != "" {
		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, cfg.Message, false, false), nil, nil,
		))
	}

	if cfg.Context != "" {
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject(slack.MarkdownType, cfg.Context, false, false),
		))
	}

	fallback := cfg.Message
	if fallback == "" {
		fallback = cfg.Title
	}

	attachment := slack.Attachment{
		Fallback: fallback,
		Blocks:   slack.Blocks{BlockSet: blocks},
		Color:    cfg.Color,
	}

	return slack.MsgOptionAttachments(attachment)
}

func prependSlackMention(ctx context.Context, cfg config, httpClient *http.Client) string {
	message := cfg.Message
	if cfg.MappingEndpoint == "" {
		slog.Info("GITHUB_SLACK_MAPPING_ENDPOINT not set, skipping mention")
		return message
	}

	if !cfg.EnableMentions {
		slog.Info("Slack mentions disabled via ENABLE_SLACK_MENTIONS, skipping mention")
		return message
	}

	ghUser := cfg.GitHubUser
	if ghUser == "" {
		slog.Info("GH_USER empty, skipping mention")
		return message
	}

	if !containsGitHubUsername(message, ghUser) {
		slog.Info("GitHub username not found in message, skipping mention", "github_user", ghUser)
		return message
	}

	slackID, err := fetchSlackUserID(ctx, httpClient, ghUser, cfg.MappingEndpoint)
	if err != nil {
		var notFoundErr *slackUserNotFoundError
		if errors.As(err, &notFoundErr) {
			slog.Warn("GitHub user not found in mapping API, skipping mention", "github_user", notFoundErr.GitHubUser)
		} else {
			slog.Error("Failed to fetch Slack user, skipping mention", "github_user", ghUser, "error", err)
		}
		return message
	}

	slog.Info("Slack ID found", "github_user", ghUser, "slack_user_id", slackID)
	return fmt.Sprintf("<@%s>: %s", slackID, message)
}

// extractMentionedUserIDs returns the unique Slack user IDs mentioned in the
// message, preserving first-seen order.
func extractMentionedUserIDs(message string) []string {
	matches := slackMentionRe.FindAllStringSubmatch(message, -1)
	seen := make(map[string]struct{}, len(matches))
	var ids []string
	for _, m := range matches {
		if _, ok := seen[m[1]]; ok {
			continue
		}
		seen[m[1]] = struct{}{}
		ids = append(ids, m[1])
	}
	return ids
}

// ensureMentionMembership invites or notifies (per mode) the users tagged in the
// message. It is best-effort: every failure is logged, none is fatal.
func ensureMentionMembership(ctx context.Context, client slackMembershipClient, mode membershipMode, channelID, message string) {
	if mode == membershipModeNone {
		return
	}

	ids := extractMentionedUserIDs(message)
	if len(ids) == 0 {
		slog.Info("No Slack mentions found, skipping membership step")
		return
	}

	switch mode {
	case membershipModeInvite:
		inviteMentionedUsers(ctx, client, channelID, ids)
	case membershipModeNotify:
		notifyMentionedUsers(ctx, client, channelID, ids)
	}
}

// inviteMentionedUsers invites each user individually. conversations.invite is
// all-or-nothing per call, so a single already-member id would otherwise block
// inviting everyone else.
func inviteMentionedUsers(ctx context.Context, client slackMembershipClient, channelID string, ids []string) {
	for _, id := range ids {
		_, err := client.InviteUsersToConversationContext(ctx, channelID, id)
		if err == nil {
			slog.Info("Invited user to channel", "channel_id", channelID, "user", id)
			continue
		}

		switch err.Error() {
		case "already_in_channel", "cant_invite_self", "user_is_bot":
			slog.Info("Invite no-op", "reason", err.Error(), "user", id)
		default:
			slog.Warn("Failed to invite user to channel", "channel_id", channelID, "user", id, "error", err)
		}
	}
}

// notifyMentionedUsers DMs each mentioned user a link to the channel.
func notifyMentionedUsers(ctx context.Context, client slackMembershipClient, channelID string, ids []string) {
	for _, id := range ids {
		ch, _, _, err := client.OpenConversationContext(ctx, &slack.OpenConversationParameters{Users: []string{id}})
		if err != nil {
			slog.Warn("Failed to open DM", "user", id, "error", err)
			continue
		}

		text := fmt.Sprintf("You were mentioned in <#%s>.", channelID)
		if _, _, err := client.PostMessageContext(ctx, ch.ID, slack.MsgOptionText(text, false)); err != nil {
			slog.Warn("Failed to send DM", "user", id, "error", err)
		}
	}
}

func containsGitHubUsername(message, username string) bool {
	pattern := fmt.Sprintf(`(?i)(^|%s)%s(%s|$)`, usernameBoundaryClass, username, usernameBoundaryClass)
	re := regexp.MustCompile(pattern)
	return re.MatchString(message)
}

type slackMappingResponse struct {
	SlackUserID string `json:"slack_user_id"`
}

func fetchSlackUserID(ctx context.Context, httpClient *http.Client, ghUser string, endpoint string) (string, error) {
	url := endpoint + ghUser
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create mapping request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("execute mapping request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read mapping response: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return "", &slackUserNotFoundError{GitHubUser: ghUser}
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mapping API returned status %d: %s", resp.StatusCode, body)
	}

	var payload slackMappingResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("unmarshal mapping response: %w", err)
	}

	return payload.SlackUserID, nil
}
