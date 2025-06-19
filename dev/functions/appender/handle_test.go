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
	data := &payload{Sequence: 1, Message: "hakuna matata"}
	e.SetData(cloudevents.ApplicationJSON, data)

	// Act
	echo, err := Handle(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}

	// Assert
	if echo == nil {
		t.Fatalf("received nil event") // fail on nil
	}

	response := &payload{}
	if err := echo.DataAs(&response); err != nil {
		t.Fatalf("error while unmarshalling data: %s", err.Error())
	} else {
		t.Logf("Response data: %+v", response)
	}
}
