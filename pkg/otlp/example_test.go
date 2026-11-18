package otlp_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/store"
	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/otlp"
)

// exampleCollector shows the intended use from a collector's Setup.
type exampleCollector struct {
	addr string
	srv  *otlp.Server
}

func (c *exampleCollector) Setup(ctx context.Context, logger *slog.Logger, s store.CollectorStore) error {
	srv, err := otlp.Start(ctx, otlp.Config{
		Logger: logger,
		Store:  s,
		Endpoints: []otlp.Endpoint{{
			Name:    "device",
			Addr:    c.addr,
			Signals: []store.Signal{store.SignalLogs},
			Processor: func(_ context.Context, _ otlp.Request, records []store.NewTelemetryRecord) ([]store.NewTelemetryRecord, error) {
				for i := range records {
					records[i].Attributes["source"] = "device"
				}
				return records, nil
			},
		}},
	})
	c.srv = srv
	return err
}

func Example() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := &fakeStore{}
	c := &exampleCollector{addr: "127.0.0.1:0"}
	if err := c.Setup(ctx, slog.New(slog.DiscardHandler), st); err != nil {
		fmt.Println("setup:", err)
		return
	}

	url := "http://" + c.srv.Addr("device").String() + "/v1/logs"
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(validLogs))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println("post:", err)
		return
	}
	_ = resp.Body.Close()

	_, records := st.snapshot()
	fmt.Println(resp.StatusCode, len(records), records[0].Attributes["source"])

	cancel()
	fmt.Println("wait:", c.srv.Wait())
	// Output:
	// 200 1 device
	// wait: <nil>
}
