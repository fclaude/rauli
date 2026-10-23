package cdp

import (
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fake is a DevTools endpoint that answers every command with an empty result.
func fake(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			_, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			var m struct {
				ID int64 `json:"id"`
			}
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			reply, _ := json.Marshal(map[string]any{"id": m.ID, "result": map[string]any{}})
			if err := c.Write(r.Context(), websocket.MessageText, reply); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// A caller that gives up must never take the connection down with it. The
// WebSocket library closes a connection whose write is cut off halfway, so a
// context that ends while a command is being sent must not reach the write.
func TestACallThatIsCancelledDoesNotCloseTheConnection(t *testing.T) {
	c, err := Dial(context.Background(), fake(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			r := rand.New(rand.NewSource(seed))
			for i := 0; i < 1500; i++ {
				ctx, cancel := context.WithCancel(context.Background())
				// End the call at a random moment, from before it starts to after it finishes.
				go func(d time.Duration) { time.Sleep(d); cancel() }(time.Duration(r.Intn(300)) * time.Microsecond)
				_ = c.Call(ctx, "", "Test.ping", map[string]any{"pad": strings.Repeat("x", 4096)}, nil)
				cancel()
			}
		}(int64(g))
	}
	wg.Wait()

	if err := c.Err(); err != nil {
		t.Fatalf("cancelled calls closed the connection: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Call(ctx, "", "Test.ping", nil, nil); err != nil {
		t.Fatalf("the connection does not work after cancelled calls: %v", err)
	}
}

func TestCallsAnswerAndEventsReachSubscribers(t *testing.T) {
	c, err := Dial(context.Background(), fake(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out map[string]any
	if err := c.Call(ctx, "", "Test.ping", nil, &out); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(ctx, "", "Test.ping", nil, nil); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err := c.Call(ctx, "", "Test.ping", nil, nil); err == nil {
		t.Fatal("a call on a closed connection should fail")
	}
}
