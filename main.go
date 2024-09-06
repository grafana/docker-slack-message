package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"

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
	Token             string `envconfig:"SLACK_TOKEN" required:"true"`
}

func (c config) String() string {
	c.Token = c.Token[:8] + "..."
	json, _ := json.MarshalIndent(c, "", "  ")
	return string(json)
}

func main() {
	var cfg config
	envconfig.MustProcess("", &cfg)
	log.Printf("Config: %s\n", cfg)

	client := slack.New(cfg.Token)

	// Send the message
	options := []slack.MsgOption{content(cfg)}
	if cfg.UpdateTs != "" {
		options = append(options, slack.MsgOptionUpdate(cfg.UpdateTs))
	} else if cfg.ThreadTs != "" {
		options = append(options, slack.MsgOptionTS(cfg.ThreadTs))
		if cfg.AlsoSendToChannel {
			options = append(options, slack.MsgOptionBroadcast())
		}
	}
	channelID, messageTs, _, err := client.SendMessage(cfg.Channel, options...)
	if err != nil {
		panic(err)
	}

	// If we already had a threadTs passed in (via env), keep using that one so
	// that replies are threaded to the original thread. Replying to a reply
	// doesn't work.
	threadTs := cfg.ThreadTs
	if threadTs == "" {
		threadTs = messageTs
	}

	// Write the messageTs and threadTs to a file to be reused in another container (For example, steps in Argo Workflows)
	// ThreadTs: timestamp of the root message of a thread
	// MessageTs: timestamp of the message (root or reply)
	// ChannelID: ID of the channel where the message was sent. This is required to update messages. The API requires the ID, not the name.
	if cfg.OutputDir != "" {
		log.Printf("channel-id: %s\n", channelID)
		if err := os.WriteFile(filepath.Join(cfg.OutputDir, "channel-id"), []byte(channelID), 0644); err != nil {
			panic(err)
		}
		log.Printf("message-ts: %s\n", messageTs)
		if err := os.WriteFile(filepath.Join(cfg.OutputDir, "message-ts"), []byte(messageTs), 0644); err != nil {
			panic(err)
		}
		log.Printf("thread-ts: %s\n", threadTs)
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
