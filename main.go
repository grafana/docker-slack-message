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
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/kelseyhightower/envconfig"
	"github.com/slack-go/slack"
)

type config struct {
	// Server settings. These are never read from the request body: the bot
	// token in particular must only ever come from the environment.
	ServerMode bool   `envconfig:"SLACK_SERVER_MODE" default:"false" json:"-"`
	ServerAddr string `envconfig:"SLACK_SERVER_ADDR" default:":8080" json:"-"`
	Token      string `envconfig:"SLACK_TOKEN" required:"true" json:"-"`
	OutputDir  string `envconfig:"SLACK_OUTPUT_DIR" default:"/app/outputs" json:"-"`
	// MappingEndpoint is infrastructure-level config shared across messages, so
	// it is read from the environment in both modes rather than per request.
	MappingEndpoint string `envconfig:"GITHUB_SLACK_MAPPING_ENDPOINT" json:"-"`

	// Content. In CLI mode these come from environment variables; in server mode
	// they come from the JSON request body.
	Color   string `envconfig:"SLACK_COLOR" default:"#008000" json:"color"`
	Title   string `envconfig:"SLACK_TITLE" json:"title"`
	Message string `envconfig:"SLACK_MESSAGE" json:"message"`
	Context string `envconfig:"SLACK_CONTEXT" json:"context"`

	AlsoSendToChannel bool   `envconfig:"SLACK_ALSO_SEND_TO_CHANNEL" default:"false" json:"also_send_to_channel"`
	Channel           string `envconfig:"SLACK_CHANNEL" json:"channel"`
	ThreadTs          string `envconfig:"SLACK_THREAD_TS" json:"thread_ts"`
	UpdateTs          string `envconfig:"SLACK_UPDATE_MESSAGE_TS" json:"update_message_ts"`
	DeleteTs          string `envconfig:"SLACK_DELETE_MESSAGE_TS" json:"delete_message_ts"`
	GitHubUser        string `envconfig:"GH_USER" json:"gh_user"`
	EnableMentions    bool   `envconfig:"ENABLE_SLACK_MENTIONS" json:"enable_mentions"`

	// MentionMembershipMode controls what happens to Slack users tagged in the
	// message: "none" (default, no-op), "invite" (add them to the channel) or
	// "notify" (DM them a link to the channel).
	MentionMembershipMode string `envconfig:"SLACK_MENTION_MEMBERSHIP_MODE" default:"none" json:"mention_membership_mode"`
}

const (
	slackMentionTimeout     = 30 * time.Second
	usernameBoundaryClass   = `[^A-Za-z0-9_-]`
	serverShutdownTimeout   = 10 * time.Second
	serverReadHeaderTimeout = 10 * time.Second
	maxRequestBodyBytes     = 1 << 20 // 1 MiB
)

// validationError marks a request-level problem (bad input) as opposed to an
// internal/Slack failure, so the HTTP server can answer 400 instead of 500.
type validationError struct{ err error }

func (e *validationError) Error() string { return e.err.Error() }

func newValidationError(format string, args ...any) error {
	return &validationError{err: fmt.Errorf(format, args...)}
}

// sendResult is the outcome of posting a message, returned to server clients as
// JSON and written to the output directory in CLI mode.
type sendResult struct {
	ChannelID string `json:"channel_id"`
	MessageTs string `json:"message_ts"`
	ThreadTs  string `json:"thread_ts"`
}

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
	c.Token = redactToken(c.Token)
	json, _ := json.MarshalIndent(c, "", "  ")
	return string(json)
}

func redactToken(token string) string {
	if len(token) <= 8 {
		return "..."
	}
	return token[:8] + "..."
}

func main() {
	var cfg config
	envconfig.MustProcess("", &cfg)
	slog.Info("Config loaded", "config", cfg.String())

	slackClient := slack.New(cfg.Token)
	httpClient := &http.Client{
		Timeout: slackMentionTimeout,
	}

	if cfg.ServerMode {
		if err := runServer(cfg, slackClient, httpClient); err != nil {
			slog.Error("Server stopped with error", "error", err)
			os.Exit(1)
		}
		return
	}

	if cfg.Channel == "" {
		slog.Error("Invalid configuration", "error", "SLACK_CHANNEL is required")
		os.Exit(1)
	}

	result, err := sendSlackMessage(context.Background(), cfg, slackClient, httpClient)
	if err != nil {
		slog.Error("Failed to send message", "error", err)
		os.Exit(1)
	}

	// Write the channelID, messageTs and threadTs to a file to be reused in another container (For example, steps in Argo Workflows)
	// ThreadTs: timestamp of the root message of a thread
	// MessageTs: timestamp of the message (root or reply)
	// ChannelID: ID of the channel where the message was sent. This is required to update messages. The API requires the ID, not the name.
	if cfg.OutputDir != "" {
		slog.Info("channel-id written", "channel_id", result.ChannelID)
		if err := os.WriteFile(filepath.Join(cfg.OutputDir, "channel-id"), []byte(result.ChannelID), 0644); err != nil {
			panic(err)
		}
		slog.Info("message-ts written", "message_ts", result.MessageTs)
		if err := os.WriteFile(filepath.Join(cfg.OutputDir, "message-ts"), []byte(result.MessageTs), 0644); err != nil {
			panic(err)
		}
		slog.Info("thread-ts written", "thread_ts", result.ThreadTs)
		if err := os.WriteFile(filepath.Join(cfg.OutputDir, "thread-ts"), []byte(result.ThreadTs), 0644); err != nil {
			panic(err)
		}
	}
}

// sendSlackMessage posts (or updates/deletes) a message according to cfg and
// returns the resulting identifiers. It is the shared core used by both the CLI
// and the HTTP server.
func sendSlackMessage(ctx context.Context, cfg config, slackClient *slack.Client, httpClient *http.Client) (sendResult, error) {
	mode, err := parseMembershipMode(cfg.MentionMembershipMode)
	if err != nil {
		return sendResult{}, &validationError{err: err}
	}

	if cfg.UpdateTs != "" && cfg.DeleteTs != "" {
		return sendResult{}, newValidationError("cannot update and delete a message at the same time")
	}

	cfg.Message = prependSlackMention(ctx, cfg, httpClient)

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

	channelID, messageTs, _, err := slackClient.SendMessageContext(ctx, cfg.Channel, options...)
	if err != nil {
		return sendResult{}, fmt.Errorf("send message: %w", err)
	}

	// Best-effort: invite or notify any users tagged in the message who may not
	// be in the channel. Only for new messages/replies — for update the original
	// send already handled it, and a delete has nothing to be mentioned in.
	if cfg.UpdateTs == "" && cfg.DeleteTs == "" {
		ensureMentionMembership(ctx, slackClient, mode, channelID, cfg.Message)
	}

	// threadTs is the timestamp of the root message of a thread.
	// If the message is a reply (threadTs passed in config), then keep the threadTs.
	// If not, this is a new thread, so the root message is the current message,
	// and the threadTs is the same as the messageTs.
	threadTs := cfg.ThreadTs
	if threadTs == "" {
		threadTs = messageTs
	}

	return sendResult{ChannelID: channelID, MessageTs: messageTs, ThreadTs: threadTs}, nil
}

// runServer starts a persistent HTTP server that posts a Slack message for each
// POST request. The bot token and other infrastructure settings come from the
// environment (serverCfg); the per-message fields come from the request body.
func runServer(serverCfg config, slackClient *slack.Client, httpClient *http.Client) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", slackMessageHandler(serverCfg, slackClient, httpClient))

	server := &http.Server{
		Addr:              serverCfg.ServerAddr,
		Handler:           mux,
		ReadHeaderTimeout: serverReadHeaderTimeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("Slack message server listening", "addr", serverCfg.ServerAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("Shutdown signal received, stopping server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

// slackMessageHandler builds an HTTP handler that decodes a per-message config
// from the request body, injects the environment-only settings (token, mapping
// endpoint), and posts the message.
func slackMessageHandler(serverCfg config, slackClient *slack.Client, httpClient *http.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Defaults that envconfig would otherwise apply for CLI mode.
		reqCfg := config{
			Color:                 "#008000",
			MentionMembershipMode: string(membershipModeNone),
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBodyBytes))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&reqCfg); err != nil {
			http.Error(w, fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)
			return
		}

		// The token and mapping endpoint always come from the environment, never
		// the request body (json:"-" prevents the body from overriding them).
		reqCfg.Token = serverCfg.Token
		reqCfg.MappingEndpoint = serverCfg.MappingEndpoint

		if reqCfg.Channel == "" {
			http.Error(w, "channel is required", http.StatusBadRequest)
			return
		}

		slog.Info("Handling message request", "channel", reqCfg.Channel)
		result, err := sendSlackMessage(r.Context(), reqCfg, slackClient, httpClient)
		if err != nil {
			var validErr *validationError
			if errors.As(err, &validErr) {
				http.Error(w, validErr.Error(), http.StatusBadRequest)
				return
			}
			slog.Error("Failed to send message", "error", err)
			http.Error(w, "failed to send message", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(result); err != nil {
			slog.Error("Failed to encode response", "error", err)
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
