package service

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestIngressLimits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits *IngressLimits
		global int
		client int
	}{
		{"defaults", nil, 2400, 240},
		{"configured", &IngressLimits{GlobalPerMinute: 10000, ClientPerMinute: 5000}, 10000, 5000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openPublicTestService(t)
			h, err := s.HandlerWithIngressLimits([]byte(`{"tools":[]}`), nil, "https://metatrash.com", tc.limits)
			if tc.limits == nil {
				// Exercise the legacy entry point as well as custom composition.
				h, err = s.Handler([]byte(`{"tools":[]}`), nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.limits != nil {
				*tc.limits = IngressLimits{GlobalPerMinute: 1, ClientPerMinute: 1}
			}
			until := time.Now().Add(time.Hour).Unix()
			request := func(ip string, want int) {
				t.Helper()
				r := httptest.NewRequest("GET", "https://metatrash.com/healthz", nil)
				r.RemoteAddr = ip + ":1234"
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != want || (want == 429 && w.Header().Get("Retry-After") == "") {
					t.Fatalf("status=%d headers=%v; want %d", w.Code, w.Header(), want)
				}
			}
			// Pin bucket expiry away from minute boundaries for deterministic checks.
			s.rates.buckets = map[string]bucket{
				"ingress":           {count: 0, until: until},
				"ingress:127.0.0.1": {count: tc.client - 1, until: until},
			}
			request("127.0.0.1", 200)
			request("127.0.0.1", 429)
			request("127.0.0.2", 200)
			s.rates.buckets["ingress"] = bucket{count: tc.global, until: until}
			request("127.0.0.3", 429)
		})
	}
}

func TestIngressLimitsInvalid(t *testing.T) {
	s := openPublicTestService(t)
	for _, limits := range []IngressLimits{{}, {1, 0}, {0, 1}, {-1, 1}, {1, -1}, {1, 2}} {
		if _, err := s.HandlerWithIngressLimits(nil, nil, "https://metatrash.com", &limits); err == nil {
			t.Fatalf("accepted invalid limits: %+v", limits)
		}
	}
}
