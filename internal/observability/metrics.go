// Package observability provides a small Prometheus text registry without extra dependencies.
package observability

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

var registry = struct {
	sync.Mutex
	values map[string]float64
}{values: map[string]float64{}}

// Labels must come from bounded values such as route templates, never request IDs.
func Labels(pairs ...string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i+1 < len(pairs); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%s=%q", pairs[i], pairs[i+1])
	}
	b.WriteByte('}')
	return b.String()
}
func Set(name, labels string, value float64) {
	registry.Lock()
	defer registry.Unlock()
	registry.values[name+labels] = value
}
func Add(name, labels string, value float64) {
	registry.Lock()
	defer registry.Unlock()
	registry.values[name+labels] += value
}
func Delete(name, labels string) {
	registry.Lock()
	defer registry.Unlock()
	delete(registry.values, name+labels)
}
func Duration(labels string, elapsed time.Duration) {
	seconds := elapsed.Seconds()
	Add("gobank_http_request_duration_seconds_sum", labels, seconds)
	Add("gobank_http_request_duration_seconds_count", labels, 1)
	for _, bound := range []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60} {
		value := 0.0
		if seconds <= bound {
			value = 1
		}
		Add("gobank_http_request_duration_seconds_bucket", strings.TrimSuffix(labels, "}")+fmt.Sprintf(",le=%q}", fmt.Sprint(bound)), value)
	}
	Add("gobank_http_request_duration_seconds_bucket", strings.TrimSuffix(labels, "}")+`,le="+Inf"}`, 1)
}
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		registry.Lock()
		defer registry.Unlock()
		keys := make([]string, 0, len(registry.values))
		for key := range registry.values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(w, "%s %g\n", key, registry.values[key])
		}
	})
}
