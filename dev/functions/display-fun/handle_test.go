package function

import (
	"context"
	"testing"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/cloudevents/sdk-go/v2/event"
)

// TestHandle ensures that Handle accepts a valid CloudEvent without error.
func TestHandle(t *testing.T) {
	// Assemble
	e := event.New()
	e.SetID("id")
	e.SetType("type")
	e.SetSource("source")
	vote1 := &EmojiVote{Emoji: Emoji{Shortcode: ":dog:", Unicode: "🐶"}, Count: 33}
	vote2 := &EmojiVote{Emoji: Emoji{Shortcode: ":dog:", Unicode: "🐶"}, Count: 33}
	data := []EmojiVote{*vote1, *vote2}
	e.SetData(cloudevents.ApplicationJSON, data)

	// Act
	echo, err := Handle(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}

	// Assert
	if echo == nil {
		t.Errorf("received nil event") // fail on nil
		return
	}

	response := []EmojiVote{}
	if err := echo.DataAs(&response); err != nil {
		t.Errorf("the received event expected data to be 'data', got '%s'", echo.Data())
	} else {
		t.Logf("Response data: %+v", response)
	}
}
