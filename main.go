package main

import (
	"encoding/json"
	"fmt"
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
	fmt.Printf("Config: %s\n", cfg)

	client := slack.New(cfg.Token)

	// Send the message
	options := []slack.MsgOption{content(cfg)}
	if cfg.ThreadTs != "" {
		options = append(options, slack.MsgOptionTS(cfg.ThreadTs))
		if cfg.AlsoSendToChannel {
			options = append(options, slack.MsgOptionBroadcast())
		}
	}
	_, threadTs, _, err := client.SendMessage(cfg.Channel, options...)
	if err != nil {
		panic(err)
	}

	// Write the threadTs to a file to be reused in another container (For example, steps in Argo Workflows)
	if cfg.OutputDir != "" {
		if err := os.WriteFile(filepath.Join(cfg.OutputDir, "thread-ts"), []byte(fmt.Sprintf("%s", threadTs)), 0644); err != nil {
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

	attachment := slack.Attachment{
		Blocks: slack.Blocks{BlockSet: blocks},
		Color:  cfg.Color,
	}

	return slack.MsgOptionAttachments(attachment)
}
