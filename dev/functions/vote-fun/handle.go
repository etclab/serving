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
	env        envConfig
	emojiVotes map[string]int
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
	// TODO: initialize from a store/db
	emojiVotes = make(map[string]int)
	for _, code := range top100Emoji {
		emojiVotes[code] = 0
	}
}

// Handle an event.
func Handle(ctx context.Context, inputEvent event.Event) (*event.Event, error) {

	// receive emoji from the input event
	data := &Emoji{}
	if err := inputEvent.DataAs(data); err != nil {
		log.Printf("Got error while unmarshalling data: %s", err.Error())
		return nil, http.NewResult(400, "got error while unmarshalling data: %w", err)
	}

	// TODO: ensure proper locking before updating the map
	// update the vote for emoji
	emojiVotes[data.Shortcode]++

	emojiVote := &EmojiVote{
		Count: emojiVotes[data.Shortcode],
		Emoji: *data,
	}
	outputEvent := inputEvent.Clone()

	// event type
	if env.Type != "" {
		outputEvent.SetType(env.Type)
	}

	if err := outputEvent.SetData(cloudevents.ApplicationJSON, emojiVote); err != nil {
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
