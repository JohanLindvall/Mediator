package dlna

import (
	"encoding/xml"
	"net/url"
	"strings"
	"testing"
	"time"
)

// unmarshal keeps the description tests to what they are about.
func unmarshal(t *testing.T, doc string, v any) error {
	t.Helper()
	return xml.Unmarshal([]byte(doc), v)
}

func TestFormatTime(t *testing.T) {
	for _, c := range []struct {
		in   time.Duration
		want string
	}{
		{0, "0:00:00"},
		{83 * time.Second, "0:01:23"},
		{2*time.Hour + 5*time.Minute + 9*time.Second, "2:05:09"},
		{-5 * time.Second, "0:00:00"},
		{1500 * time.Millisecond, "0:00:01"},
	} {
		if got := FormatTime(c.in); got != c.want {
			t.Errorf("FormatTime(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseTime(t *testing.T) {
	for _, c := range []struct {
		in   string
		want time.Duration
	}{
		{"0:01:23", 83 * time.Second},
		{"2:05:09", 2*time.Hour + 5*time.Minute + 9*time.Second},
		{"0:00:01.500", 1500 * time.Millisecond},
		{"01:23", 83 * time.Second},
		// A set that does not know says so, and a poll must read that as
		// "no position" rather than as an error it has to handle.
		{"NOT_IMPLEMENTED", 0},
		{"", 0},
		{"nonsense", 0},
	} {
		if got := ParseTime(c.in); got != c.want {
			t.Errorf("ParseTime(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestOutArgsReadsResponse(t *testing.T) {
	doc := []byte(`<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>` +
		`<u:GetPositionInfoResponse xmlns:u="urn:schemas-upnp-org:service:AVTransport:1">` +
		`<Track>1</Track><TrackDuration>0:59:57</TrackDuration><RelTime>0:00:16</RelTime>` +
		`<TrackURI>http://192.0.2.9:8080/api/stream/abc</TrackURI>` +
		`<TrackMetaData>&lt;DIDL-Lite&gt;&lt;RelTime&gt;lies&lt;/RelTime&gt;&lt;/DIDL-Lite&gt;</TrackMetaData>` +
		`</u:GetPositionInfoResponse></s:Body></s:Envelope>`)
	out := outArgs(doc)
	if out["TrackDuration"] != "0:59:57" || out["RelTime"] != "0:00:16" {
		t.Fatalf("times not read: %v", out)
	}
	if out["TrackURI"] != "http://192.0.2.9:8080/api/stream/abc" {
		t.Errorf("TrackURI = %q", out["TrackURI"])
	}
	// The metadata is a document inside a value; it must not be read as
	// values of its own, or a nested name would overwrite a real answer.
	if out["RelTime"] != "0:00:16" {
		t.Errorf("metadata leaked into the arguments: %v", out)
	}
}

func TestOutArgsReadsFault(t *testing.T) {
	doc := []byte(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault>` +
		`<faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring><detail>` +
		`<UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>701</errorCode>` +
		`<errorDescription>Transition not available</errorDescription></UPnPError>` +
		`</detail></s:Fault></s:Body></s:Envelope>`)
	out := outArgs(doc)
	if out["errorCode"] != "701" || !strings.Contains(out["errorDescription"], "Transition") {
		t.Fatalf("fault not read: %v", out)
	}
}

const description = `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0"><device>
<friendlyName>The television</friendlyName>
<UDN>uuid:561bd0b7-0c6a-dcb1-fffc-19fcec9682ed</UDN>
<serviceList>
 <service><serviceType>urn:schemas-upnp-org:service:AVTransport:1</serviceType>
  <controlURL>/AVTransport/control.xml</controlURL></service>
 <service><serviceType>urn:schemas-upnp-org:service:RenderingControl:1</serviceType>
  <controlURL>/RenderingControl/control.xml</controlURL></service>
</serviceList></device></root>`

func TestRendererFrom(t *testing.T) {
	base, _ := url.Parse("http://192.0.2.9:1356/desc.xml")
	var root xmlRoot
	if err := unmarshal(t, description, &root); err != nil {
		t.Fatal(err)
	}
	r := rendererFrom(root.Device, base)
	if r == nil {
		t.Fatal("no renderer found in a description that has one")
	}
	if r.Name != "The television" {
		t.Errorf("Name = %q", r.Name)
	}
	// Relative control URLs are resolved against where the description came
	// from; a device is under no obligation to write absolute ones.
	if r.control[avTransport] != "http://192.0.2.9:1356/AVTransport/control.xml" {
		t.Errorf("control URL = %q", r.control[avTransport])
	}
	if !r.CanControlVolume() {
		t.Error("volume control missed")
	}
	// The identity is the device's, not its address: a lease change must not
	// turn one television into two.
	if len(r.ID) != 16 {
		t.Errorf("ID = %q", r.ID)
	}
	other := *r
	if r.ID != other.ID {
		t.Error("id not stable")
	}
}

// A renderer nested inside a container device is still a renderer.
const nested = `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0"><device>
<friendlyName>A box of things</friendlyName><UDN>uuid:outer</UDN>
<deviceList><device><friendlyName>Inner</friendlyName><UDN>uuid:inner</UDN>
<serviceList><service><serviceType>urn:schemas-upnp-org:service:AVTransport:1</serviceType>
<controlURL>http://192.0.2.9:80/ctl</controlURL></service></serviceList>
</device></deviceList></device></root>`

func TestRendererFromNested(t *testing.T) {
	base, _ := url.Parse("http://192.0.2.9:1356/desc.xml")
	var root xmlRoot
	if err := unmarshal(t, nested, &root); err != nil {
		t.Fatal(err)
	}
	r := rendererFrom(root.Device, base)
	if r == nil || r.Name != "Inner" {
		t.Fatalf("nested renderer not found: %+v", r)
	}
}

// A device with no transport is not something we can drive, and offering it
// would be a receiver that does nothing when chosen.
const speakerless = `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0"><device>
<friendlyName>Not a renderer</friendlyName><UDN>uuid:x</UDN>
<serviceList><service><serviceType>urn:schemas-upnp-org:service:ContentDirectory:1</serviceType>
<controlURL>/ctl</controlURL></service></serviceList></device></root>`

func TestRendererFromWithoutTransport(t *testing.T) {
	base, _ := url.Parse("http://192.0.2.9:1356/desc.xml")
	var root xmlRoot
	if err := unmarshal(t, speakerless, &root); err != nil {
		t.Fatal(err)
	}
	if r := rendererFrom(root.Device, base); r != nil {
		t.Fatalf("a content directory is not a renderer: %+v", r)
	}
}

func TestAccepts(t *testing.T) {
	r := &Renderer{sinks: map[string]bool{"video/x-matroska": true, "video/mp4": true}}
	if !r.Accepts("video/x-matroska") || !r.Accepts("VIDEO/MP4") {
		t.Error("listed types must be accepted, whatever their case")
	}
	if r.Accepts("video/webm") {
		t.Error("a type it did not list is not accepted")
	}
	// A set that would not say what it plays is not a set that plays
	// nothing: refusing on its behalf is the worse failure.
	if !(&Renderer{}).Accepts("video/webm") {
		t.Error("an unknown answer must be a yes")
	}
}

func TestMetadata(t *testing.T) {
	md := Metadata(Meta{
		Title:    `Rock & Roll <"live">`,
		Class:    UPnPClass("video"),
		MIME:     "video/x-matroska",
		URI:      "http://192.0.2.4:8087/api/stream/abc",
		Duration: 90 * time.Minute,
	})
	if strings.Contains(md, `<"live">`) {
		t.Error("the title is not escaped")
	}
	if !strings.Contains(md, "Rock &amp; Roll") {
		t.Errorf("title missing: %s", md)
	}
	// Byte-range seeking is what makes the television's own remote work on
	// our stream rather than restarting the file.
	if !strings.Contains(md, "DLNA.ORG_OP=01") {
		t.Errorf("seek capability not declared: %s", md)
	}
	if !strings.Contains(md, `duration="1:30:00"`) {
		t.Errorf("duration missing: %s", md)
	}
	if !strings.Contains(md, "object.item.videoItem") {
		t.Errorf("class missing: %s", md)
	}
	if strings.Contains(Metadata(Meta{Title: "x", MIME: "audio/mpeg", URI: "u"}), "duration=") {
		t.Error("an unmeasured file must not claim a duration")
	}
}

// A television playing music shows what this document says and nothing
// else: it has been handed one file and has no library to look anything up
// in. Without these it shows its own logo on a black screen.
func TestMetadataForMusic(t *testing.T) {
	md := Metadata(Meta{
		Title:    "Undoing",
		Class:    UPnPClass("audio"),
		MIME:     "audio/mpeg",
		URI:      "http://192.0.2.4:8087/api/stream/abc",
		Duration: 100 * time.Second,
		Artist:   "A & B",
		Album:    "Servants",
		Art:      "http://192.0.2.4:8087/api/thumb/abc?w=640",
		Genre:    "Black Metal",
		Year:     2017,
		Track:    6,
		Size:     1_000_000,
	})
	for _, want := range []string{
		"<upnp:genre>Black Metal</upnp:genre>",
		"<dc:date>2017-01-01</dc:date>",
		"<upnp:originalTrackNumber>6</upnp:originalTrackNumber>",
		`size="1000000"`,
		// Bytes per second, which is what UPnP means by bitrate however odd
		// that is: 1 MB over 100 s is 10000, not 80000.
		`bitrate="10000"`,
		"<upnp:artist>A &amp; B</upnp:artist>",
		"<dc:creator>A &amp; B</dc:creator>",
		"<upnp:album>Servants</upnp:album>",
		"albumArtURI",
		"thumb/abc?w=640",
		"object.item.audioItem.musicTrack",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %s in %s", want, md)
		}
	}
	// The namespace the art's profile attribute is in has to be declared, or
	// the document is not well formed and a strict renderer refuses it.
	if !strings.Contains(md, `xmlns:dlna=`) {
		t.Errorf("dlna namespace not declared: %s", md)
	}
	if err := xml.Unmarshal([]byte(md), new(struct{})); err != nil {
		t.Errorf("not well formed: %v", err)
	}

	// A film is showing the film; artwork would be for a set to ignore, and
	// an empty element is worse than none.
	plain := Metadata(Meta{Title: "x", Class: UPnPClass("video"), MIME: "video/mp4", URI: "u"})
	if strings.Contains(plain, "albumArtURI") || strings.Contains(plain, "upnp:artist") {
		t.Errorf("empty fields written out: %s", plain)
	}
}
