package function

import (
	"context"
	"log"
	"os"
	"sort"
	"sync"

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
	emojiVotes EmojiVotes
)

type EmojiVotes struct {
	mu   sync.RWMutex
	data map[string]int
}

func NewSafeMap() *EmojiVotes {
	return &EmojiVotes{
		data: make(map[string]int),
	}
}

func (sm *EmojiVotes) Set(key string, value int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.data[key] = value
}

func (sm *EmojiVotes) Get(key string) (int, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	val, ok := sm.data[key]
	return val, ok
}

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
	emojiVotes = *NewSafeMap()
	for _, code := range top100Emoji {
		emojiVotes.Set(code, 0)
	}
}

// Handle an event.
func Handle(ctx context.Context, inputEvent event.Event) (*event.Event, error) {
	// receive emoji from the input event
	data := &EmojiVote{}
	if err := inputEvent.DataAs(data); err != nil {
		log.Printf("Got error while unmarshalling data: %s", err.Error())
		return nil, http.NewResult(400, "got error while unmarshalling data: %w", err)
	}

	// get the new vote for emoji
	emojiVotes.Set(data.Shortcode, data.Count)

	allVotes := make([]EmojiVote, 0, len(top100Emoji))
	for _, shortCode := range top100Emoji {
		votes, _ := emojiVotes.Get(shortCode)
		allVotes = append(allVotes, EmojiVote{
			Emoji: Emoji{
				Shortcode: shortCode,
				Unicode:   emojiCodeMap[shortCode],
			},
			Count: votes,
		})
	}

	sort.Slice(allVotes, func(i, j int) bool {
		// descending order by count
		return allVotes[i].Count > allVotes[j].Count
	})

	outputEvent := inputEvent.Clone()

	// event type
	if env.Type != "" {
		outputEvent.SetType(env.Type)
	}

	if err := outputEvent.SetData(cloudevents.ApplicationJSON, allVotes); err != nil {
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
