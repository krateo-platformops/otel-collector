// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package k8sobjectsreceiver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/receiver"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// Once for the whole run: watch goroutines read these two until they end.
func TestMain(m *testing.M) {
	watchRetryInitial, watchRetryMax = 10*time.Millisecond, 50*time.Millisecond
	os.Exit(m.Run())
}

// apiServer is the two calls the watch mode makes, over HTTP, so the real client and RetryWatcher run.
// list and watch decide each call by its number, from 1.
type apiServer struct {
	mu             sync.Mutex
	lists, watches int
	list           func(n int) int // HTTP status
	watch          func(n int) []map[string]any
}

func (a *apiServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	a.mu.Lock()
	watching := r.URL.Query().Get("watch") != ""
	if watching {
		a.watches++
	} else {
		a.lists++
	}
	lists, watches := a.lists, a.watches
	a.mu.Unlock()
	if !watching {
		code := a.list(lists)
		w.WriteHeader(code)
		if code != http.StatusOK {
			fmt.Fprintf(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"ServiceUnavailable","code":%d}`, code)
			return
		}
		fmt.Fprintf(w, `{"kind":"EventList","apiVersion":"v1","metadata":{"resourceVersion":"%d"},"items":[]}`, 100*lists)
		return
	}
	w.WriteHeader(http.StatusOK)
	for _, ev := range a.watch(watches) {
		_ = json.NewEncoder(w).Encode(ev)
	}
	w.(http.Flusher).Flush()
	<-r.Context().Done() // the watch stays open, as a real one does
}

func (a *apiServer) calls() (int, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lists, a.watches
}

func event(name string, rv int) map[string]any {
	return map[string]any{"type": "ADDED", "object": map[string]any{
		"kind": "Event", "apiVersion": "v1",
		"metadata": map[string]any{"name": name, "namespace": "default", "resourceVersion": strconv.Itoa(rv)},
	}}
}

func gone() map[string]any {
	return map[string]any{"type": "ERROR", "object": map[string]any{
		"kind": "Status", "apiVersion": "v1", "status": "Failure", "reason": "Expired", "code": http.StatusGone,
	}}
}

func startWatching(t *testing.T, api *apiServer) (*atomic.Int64, *observer.ObservedLogs, func()) {
	t.Helper()
	srv := httptest.NewServer(api)
	cfg := createDefaultConfig().(*Config)
	cfg.makeDynamicClient = func() (dynamic.Interface, error) { return dynamic.NewForConfig(&rest.Config{Host: srv.URL, QPS: -1}) } // no client throttle: it would hide a tight loop
	cfg.Objects = []*K8sObjectsConfig{{Name: "events", Mode: WatchMode, gvr: &schema.GroupVersionResource{Version: "v1", Resource: "events"}}}
	core, logs := observer.New(zap.InfoLevel)
	set := receiver.Settings{ID: component.NewID(component.MustNewType("k8sobjects")), TelemetrySettings: component.TelemetrySettings{
		Logger: zap.New(core), MeterProvider: metricnoop.NewMeterProvider(), TracerProvider: tracenoop.NewTracerProvider(),
	}}
	received := new(atomic.Int64)
	sink, err := consumer.NewLogs(func(_ context.Context, ld plog.Logs) error {
		received.Add(int64(ld.LogRecordCount()))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rcv, err := newReceiver(set, cfg, sink)
	if err != nil {
		t.Fatal(err)
	}
	if err := rcv.Start(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	return received, logs, func() {
		_ = rcv.Shutdown(context.Background())
		srv.CloseClientConnections()
		srv.Close()
	}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("never happened: %s", what)
}

func ok(int) int { return http.StatusOK }

// The API server restarts: the watch ends with 410, and the list that should restart it fails while
// the server is coming back. The receiver must keep trying and then deliver events again.
func TestWatchSurvivesListFailuresAfterTheWatchEnds(t *testing.T) {
	api := &apiServer{
		list: func(n int) int {
			if n >= 2 && n <= 4 {
				return http.StatusServiceUnavailable
			}
			return http.StatusOK
		},
		watch: func(n int) []map[string]any {
			if n == 1 {
				return []map[string]any{event("before", 101), gone()}
			}
			return []map[string]any{event("after", 601)}
		},
	}
	received, logs, stop := startWatching(t, api)
	defer stop()
	eventually(t, "the event after the outage", func() bool { return received.Load() >= 2 })
	if lists, _ := api.calls(); lists < 5 {
		t.Fatalf("%d lists: the outage was not exercised", lists)
	}
	if logs.FilterMessageSnippet("could not retrieve a resourceVersion").Len() != 3 {
		t.Fatalf("want one error per failed list, got %v", logs.All())
	}
}

// The collector starts while the API server, or its RBAC, is not ready.
func TestWatchSurvivesAFailedFirstList(t *testing.T) {
	for _, code := range []int{http.StatusServiceUnavailable, http.StatusForbidden} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			api := &apiServer{
				list: func(n int) int {
					if n <= 3 {
						return code
					}
					return http.StatusOK
				},
				watch: func(int) []map[string]any { return []map[string]any{event("first", 401)} },
			}
			received, _, stop := startWatching(t, api)
			defer stop()
			eventually(t, "the first event", func() bool { return received.Load() >= 1 })
		})
	}
}

// A watch that ends at once, every time, must not become a tight loop of lists.
func TestWatchBacksOffWhenEveryWatchEndsAtOnce(t *testing.T) {
	api := &apiServer{list: ok, watch: func(int) []map[string]any { return []map[string]any{gone()} }}
	_, _, stop := startWatching(t, api)
	time.Sleep(time.Second)
	lists, _ := api.calls()
	stop()
	// 10, 20, 40, 50, 50... ms: about 22 in a second. Unbounded it is in the thousands.
	if lists < 5 || lists > 60 {
		t.Fatalf("%d lists in a second", lists)
	}
}

// Shutdown must end the retries: nothing may list after it.
func TestShutdownEndsTheRetries(t *testing.T) {
	api := &apiServer{list: func(int) int { return http.StatusServiceUnavailable }, watch: func(int) []map[string]any { return nil }}
	_, logs, stop := startWatching(t, api)
	eventually(t, "a few failed lists", func() bool { n, _ := api.calls(); return n >= 3 })
	stop()
	time.Sleep(200 * time.Millisecond) // a retry already under way may still log once
	at := logs.Len()
	time.Sleep(300 * time.Millisecond)
	if now := logs.Len(); now != at {
		t.Fatalf("the watch is still retrying after shutdown: %d more log lines", now-at)
	}
	if logs.FilterMessageSnippet("retrying").Len() < 3 {
		t.Fatalf("the retries were not logged: %v", logs.All())
	}
}
