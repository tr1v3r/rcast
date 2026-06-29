package upnp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Note: TestEventHandlerDoesNotIssueFalseSubscription (SUBSCRIBE -> 501, empty
// SID) already lives in handlers_test.go and is not duplicated here. These
// cases cover the remaining methods.

func TestEventHandlerUnsubscribeNotImplemented(t *testing.T) {
	req := httptest.NewRequest("UNSUBSCRIBE", "/upnp/event/avtransport", nil)
	req.Header.Set("SID", "uuid:deadbeef")
	rec := httptest.NewRecorder()
	EventHandler(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("UNSUBSCRIBE status=%d, want 501; body=%s", rec.Code, rec.Body.String())
	}
	if sid := rec.Header().Get("SID"); sid != "" {
		t.Fatalf("SID=%q, want empty (must not confirm unsubscription)", sid)
	}
}

func TestEventHandlerSubscribeNoCallbacksStill501(t *testing.T) {
	// Even without CALLBACK/NT the handler must not pretend to subscribe.
	req := httptest.NewRequest("SUBSCRIBE", "/upnp/event/renderingcontrol", nil)
	rec := httptest.NewRecorder()
	EventHandler(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("SUBSCRIBE status=%d, want 501; body=%s", rec.Code, rec.Body.String())
	}
	if sid := rec.Header().Get("SID"); sid != "" {
		t.Fatalf("SID=%q, want empty", sid)
	}
}

func TestEventHandlerDisallowedMethods(t *testing.T) {
	// GET/POST/PUT/DELETE/PATCH/etc. must all return 405 + Allow header.
	disallowed := []string{
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodDelete,
		http.MethodPatch,
		http.MethodOptions,
	}
	for _, method := range disallowed {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/upnp/event/avtransport", nil)
			rec := httptest.NewRecorder()
			EventHandler(rec, req)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status=%d, want 405; body=%s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Allow"); got != "SUBSCRIBE, UNSUBSCRIBE" {
				t.Fatalf("Allow=%q, want %q", got, "SUBSCRIBE, UNSUBSCRIBE")
			}
		})
	}
}
