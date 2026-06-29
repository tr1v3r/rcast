package upnp

import (
	"strings"
	"testing"
)

func TestParseCurrentURIMetaData(t *testing.T) {
	// A realistic escaped DIDL-Lite fragment as a control point would send it.
	// The document declares the dc/upnp namespaces so the production decoder's
	// namespace-qualified field paths resolve. The outer layer is HTML-escaped
	// (so it can travel inside a SOAP CurrentURIMetaData text node); the title
	// intentionally contains no XML-special chars in the basic case so the
	// round trip is unambiguous.
	const escaped = `&lt;DIDL-Lite xmlns:dc=&quot;http://purl.org/dc/elements/1.1/&quot; xmlns:upnp=&quot;urn:schemas-upnp-org:metadata-1-0/upnp/&quot;&gt;&lt;item id=&quot;1&quot; parentID=&quot;0&quot; restricted=&quot;1&quot;&gt;&lt;dc:title&gt;Big Buck Bunny&lt;/dc:title&gt;&lt;upnp:class&gt;object.item.videoItem&lt;/upnp:class&gt;&lt;res protocolInfo=&quot;http-get:*:video/mp4:*&quot;&gt;http://example.test/big.mp4&lt;/res&gt;&lt;/item&gt;&lt;/DIDL-Lite&gt;`

	d, err := ParseCurrentURIMetaData(escaped)
	if err != nil {
		t.Fatalf("ParseCurrentURIMetaData error: %v", err)
	}
	if len(d.Items) != 1 {
		t.Fatalf("items=%d, want 1", len(d.Items))
	}
	it := d.Items[0]
	if it.Title != "Big Buck Bunny" {
		t.Errorf("title=%q, want %q", it.Title, "Big Buck Bunny")
	}
	if it.Class != "object.item.videoItem" {
		t.Errorf("class=%q, want object.item.videoItem", it.Class)
	}
	if len(it.Resources) != 1 {
		t.Fatalf("resources=%d, want 1", len(it.Resources))
	}
	res := it.Resources[0]
	if res.ProtocolInfo != "http-get:*:video/mp4:*" {
		t.Errorf("protocolInfo=%q, want http-get:*:video/mp4:*", res.ProtocolInfo)
	}
	if !strings.Contains(res.URL, "http://example.test/big.mp4") {
		t.Errorf("URL=%q, want contains base URL", res.URL)
	}
	// Item attributes survive the unescape + decode round trip.
	if it.ID != "1" || it.ParentID != "0" || it.Restricted != 1 {
		t.Errorf("attrs id=%q parentID=%q restricted=%d", it.ID, it.ParentID, it.Restricted)
	}
}

func TestParseCurrentURIMetaData_TitleWithSpecialChars(t *testing.T) {
	// The outer unescape turns &amp;amp; into &amp;, which the inner XML
	// decoder then turns into a literal &. So a title with an ampersand must
	// be doubly-escaped in the input.
	const escaped = `&lt;DIDL-Lite xmlns:dc=&quot;http://purl.org/dc/elements/1.1/&quot;&gt;&lt;item id=&quot;1&quot; restricted=&quot;1&quot;&gt;&lt;dc:title&gt;Tom &amp;amp; Jerry&lt;/dc:title&gt;&lt;/item&gt;&lt;/DIDL-Lite&gt;`
	d, err := ParseCurrentURIMetaData(escaped)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if d.Items[0].Title != "Tom & Jerry" {
		t.Errorf("title=%q, want %q", d.Items[0].Title, "Tom & Jerry")
	}
}

func TestParseCurrentURIMetaData_GarbageInputErrors(t *testing.T) {
	if _, err := ParseCurrentURIMetaData("not valid xml at all < < <"); err == nil {
		t.Fatal("expected error for garbage input, got nil")
	}
}

func TestParseCurrentURIMetaData_EmptyReturnsError(t *testing.T) {
	// Empty string is not a valid XML document.
	if _, err := ParseCurrentURIMetaData(""); err == nil {
		t.Fatal("expected error for empty input, got nil")
	}
}
