package function

import (
	"context"
	"log"
	"os"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/cloudevents/sdk-go/v2/event"
	"github.com/cloudevents/sdk-go/v2/protocol/http"
	"github.com/kelseyhightower/envconfig"
)

type envConfig struct {
	Type string `envconfig:"TYPE"`
}

var (
	env envConfig
)

type EmojiVote struct {
	Emoji
	Count int `json:"count"`
}

type Emoji struct {
	Unicode   string `json:"unicode"`
	Shortcode string `json:"shortcode"`
}

func init() {
	if err := envconfig.Process("", &env); err != nil {
		log.Printf("[ERROR] Failed to process env var: %s", err)
		os.Exit(1)
	}
}

// receives the count of all votes for each emoji
// takes the top 10 votes and outputs to a channel for display
func Handle(ctx context.Context, inputEvent event.Event) (*event.Event, error) {
	// receive emoji from the input event
	data := []EmojiVote{}
	if err := inputEvent.DataAs(&data); err != nil {
		log.Printf("Got error while unmarshalling data: %s", err.Error())
		return nil, http.NewResult(400, "got error while unmarshalling data: %w", err)
	}

	top10 := data
	data_len := len(data)
	if data_len > 10 {
		top10 = data[:10]
	}

	outputEvent := inputEvent.Clone()

	// event type
	if env.Type != "" {
		outputEvent.SetType(env.Type)
	}

	if err := outputEvent.SetData(cloudevents.ApplicationJSON, top10); err != nil {
		log.Printf("Got error while marshalling data: %s", err.Error())
		return nil, http.NewResult(500, "got error while marshalling data: %w", err)
	}

	return &outputEvent, nil
}

/*
Other supported function signatures:

	Handle()
	Handle() error
	Handle(context.Context)
	Handle(context.Context) error
	Handle(event.Event)
	Handle(event.Event) error
	Handle(context.Context, event.Event)
	Handle(context.Context, event.Event) error
	Handle(event.Event) *event.Event
	Handle(event.Event) (*event.Event, error)
	Handle(context.Context, event.Event) *event.Event
	Handle(context.Context, event.Event) (*event.Event, error)

*/
